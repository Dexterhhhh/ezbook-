package api

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"xorm.io/xorm"

	"github.com/mayswind/ezbookkeeping/pkg/core"
	"github.com/mayswind/ezbookkeeping/pkg/datastore"
	"github.com/mayswind/ezbookkeeping/pkg/errs"
	"github.com/mayswind/ezbookkeeping/pkg/hengcai"
	"github.com/mayswind/ezbookkeeping/pkg/models"
	"github.com/mayswind/ezbookkeeping/pkg/services"
	"github.com/mayswind/ezbookkeeping/pkg/uuid"
)

func summaryAccountName(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > 64 {
		runes = runes[:64]
	}
	return string(runes)
}

func createCoreInvestmentSummaryAccount(sess *xorm.Session, uid int64, investmentAccount *hengcai.InvestmentAccount) (*models.Account, error) {
	accountID := services.Accounts.GenerateUuid(uuid.UUID_TYPE_ACCOUNT)
	if accountID <= 0 {
		return nil, errors.New("无法生成主账本账户编号")
	}
	lastAccount := &models.Account{}
	has, err := sess.Cols("display_order").Where("uid = ? AND deleted = ? AND parent_account_id = ? AND category = ?", uid, false, models.LevelOneAccountParentId, models.ACCOUNT_CATEGORY_INVESTMENT).Desc("display_order").Limit(1).Get(lastAccount)
	if err != nil {
		return nil, err
	}
	displayOrder := int32(1)
	if has {
		displayOrder = lastAccount.DisplayOrder + 1
	}
	now := time.Now().Unix()
	account := &models.Account{
		AccountId: accountID, Uid: uid, Deleted: false,
		Category: models.ACCOUNT_CATEGORY_INVESTMENT, Type: models.ACCOUNT_TYPE_SINGLE_ACCOUNT,
		ParentAccountId: models.LevelOneAccountParentId, Name: summaryAccountName(investmentAccount.Name),
		DisplayOrder: displayOrder, Icon: 800, Color: "009688", Currency: strings.ToUpper(investmentAccount.BaseCurrency),
		Balance: 0, Comment: "由衡财投资模块汇总；交易和持仓明细请在投资模块维护", Extend: &models.AccountExtend{},
		Hidden: false, CreatedUnixTime: now, UpdatedUnixTime: now,
	}
	if _, err := sess.Insert(account); err != nil {
		return nil, err
	}
	return account, nil
}

func validateCoreInvestmentSummaryAccount(sess *xorm.Session, uid, accountID int64, currency string) (*models.Account, error) {
	account := &models.Account{}
	exists, err := sess.Where("uid = ? AND deleted = ? AND account_id = ?", uid, false, accountID).Get(account)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.New("关联的主账本账户不存在或已删除")
	}
	if account.Category != models.ACCOUNT_CATEGORY_INVESTMENT || account.Type != models.ACCOUNT_TYPE_SINGLE_ACCOUNT || account.ParentAccountId != models.LevelOneAccountParentId {
		return nil, errors.New("只能关联主账本中的单一投资账户")
	}
	if !strings.EqualFold(account.Currency, currency) {
		return nil, fmt.Errorf("投资账户币种 %s 与主账本账户币种 %s 不一致", currency, account.Currency)
	}
	return account, nil
}

func ensureCoreInvestmentAccountNotLinked(sess *xorm.Session, uid, investmentAccountID, coreAccountID int64) error {
	if coreAccountID <= 0 {
		return nil
	}
	duplicate := &hengcai.InvestmentAccount{}
	exists, err := sess.Where("uid = ? AND active = ? AND account_id = ? AND id <> ?", uid, true, coreAccountID, investmentAccountID).Get(duplicate)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("主账本账户已关联投资账户“%s”，不能重复关联", duplicate.Name)
	}
	return nil
}

func (a *HengcaiApi) linkInvestmentAccount(c *core.WebContext) (any, *errs.Error) {
	var input struct {
		InvestmentAccountId int64 `json:"investment_account_id"`
		AccountId           int64 `json:"account_id,string"`
		CreateCoreAccount   bool  `json:"create_core_account"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		return nil, hcError(err)
	}
	if input.InvestmentAccountId <= 0 {
		return nil, hcError(errors.New("请选择需要关联的投资账户"))
	}
	if input.CreateCoreAccount && input.AccountId > 0 {
		return nil, hcError(errors.New("自动创建和选择已有主账本账户不能同时使用"))
	}
	uid := c.GetCurrentUid()
	var investmentAccount hengcai.InvestmentAccount
	var coreAccount *models.Account
	err := datastore.Container.UserDataStore.DoTransaction(uid, c, func(sess *xorm.Session) error {
		exists, err := sess.Where("uid = ? AND id = ? AND active = ?", uid, input.InvestmentAccountId, true).Get(&investmentAccount)
		if err != nil {
			return err
		}
		if !exists {
			return errors.New("投资账户不存在、已停用或不属于当前用户")
		}
		accountID := input.AccountId
		if input.CreateCoreAccount {
			coreAccount, err = createCoreInvestmentSummaryAccount(sess, uid, &investmentAccount)
			if err != nil {
				return err
			}
			accountID = coreAccount.AccountId
		} else if accountID > 0 {
			coreAccount, err = validateCoreInvestmentSummaryAccount(sess, uid, accountID, investmentAccount.BaseCurrency)
			if err != nil {
				return err
			}
		}
		if err := ensureCoreInvestmentAccountNotLinked(sess, uid, investmentAccount.Id, accountID); err != nil {
			return err
		}
		investmentAccount.AccountId = accountID
		updated, err := sess.Where("uid = ? AND id = ?", uid, investmentAccount.Id).Cols("account_id").Update(&investmentAccount)
		if err != nil {
			return err
		}
		if updated != 1 {
			return errors.New("投资账户关联更新失败")
		}
		return nil
	})
	if err != nil {
		return nil, hcError(err)
	}
	if err := a.refreshInvestmentValuations(c, uid); err != nil {
		return nil, errs.ErrOperationFailed
	}
	result := map[string]any{"investment_account": investmentAccount, "linked": investmentAccount.AccountId > 0}
	if coreAccount != nil {
		result["core_account"] = coreAccount.ToAccountInfoResponse()
	}
	return result, nil
}

// refreshInvestmentValuations materializes one summary per investment account.
// A reconciled statement supplies the cash anchor. Later base-currency trades
// adjust that cash, while positions supply the current marked value. When no
// statement exists, the result is explicitly marked as positions-only.
func (a *HengcaiApi) refreshInvestmentValuations(c core.Context, uid int64) error {
	accounts := make([]*hengcai.InvestmentAccount, 0)
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND active = ?", uid, true).Find(&accounts); err != nil {
		return err
	}
	instruments := make([]*hengcai.InvestmentInstrument, 0)
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Find(&instruments); err != nil {
		return err
	}
	positions := make([]*hengcai.InvestmentPosition, 0)
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Find(&positions); err != nil {
		return err
	}
	transactions := make([]*hengcai.InvestmentTransaction, 0)
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Find(&transactions); err != nil {
		return err
	}
	statements := make([]*hengcai.InvestmentStatementImport, 0)
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND status = ?", uid, "RECONCILED").Desc("period_end").Desc("id").Find(&statements); err != nil {
		return err
	}

	instrumentByID := make(map[int64]*hengcai.InvestmentInstrument, len(instruments))
	for _, instrument := range instruments {
		instrumentByID[instrument.Id] = instrument
	}
	latestStatementByAccount := make(map[int64]*hengcai.InvestmentStatementImport)
	for _, statement := range statements {
		if latestStatementByAccount[statement.InvestmentAccountId] == nil {
			latestStatementByAccount[statement.InvestmentAccountId] = statement
		}
	}
	positionsByAccount := make(map[int64][]*hengcai.InvestmentPosition)
	for _, position := range positions {
		positionsByAccount[position.InvestmentAccountId] = append(positionsByAccount[position.InvestmentAccountId], position)
	}
	transactionsByAccount := make(map[int64][]*hengcai.InvestmentTransaction)
	for _, transaction := range transactions {
		transactionsByAccount[transaction.InvestmentAccountId] = append(transactionsByAccount[transaction.InvestmentAccountId], transaction)
	}

	now := time.Now().Unix()
	for _, account := range accounts {
		baseCurrency := strings.ToUpper(account.BaseCurrency)
		positionValue := int64(0)
		positionAsOf := int64(0)
		currencyMismatch := false
		costBased := false
		for _, position := range positionsByAccount[account.Id] {
			instrument := instrumentByID[position.InstrumentId]
			if instrument == nil || !strings.EqualFold(instrument.Currency, baseCurrency) {
				currencyMismatch = true
				continue
			}
			positionValue += position.MarketValueMinor
			if position.AsOfUnixTime > positionAsOf {
				positionAsOf = position.AsOfUnixTime
			}
			if position.MarketPrice <= 0 {
				costBased = true
			}
		}

		valuation := &hengcai.InvestmentAccountValuation{
			Uid: account.Uid, InvestmentAccountId: account.Id, BaseCurrency: baseCurrency,
			PositionValueMinor: positionValue, TotalEquityMinor: positionValue,
			Source: "POSITIONS", Quality: "POSITIONS_ONLY", AsOfUnixTime: positionAsOf, UpdatedUnixTime: now,
		}
		statement := latestStatementByAccount[account.Id]
		if statement != nil {
			periodEnd, parseErr := time.Parse("2006-01-02", statement.PeriodEnd)
			if parseErr == nil {
				valuation.AnchorUnixTime = periodEnd.AddDate(0, 0, 1).Add(-time.Second).Unix()
			}
			valuation.AnchorCashBalanceMinor = statement.ClosingNetAssetsMinor - statement.PortfolioValueMinor
			cashBalance := valuation.AnchorCashBalanceMinor
			for _, transaction := range transactionsByAccount[account.Id] {
				if transaction.TradedAt > valuation.AnchorUnixTime && strings.EqualFold(transaction.Currency, baseCurrency) {
					cashBalance += transaction.NetCashAmountMinor
				} else if transaction.TradedAt > valuation.AnchorUnixTime && !strings.EqualFold(transaction.Currency, baseCurrency) {
					currencyMismatch = true
				}
			}
			valuation.Source = "STATEMENT_AND_MARKET"
			valuation.Quality = "COMPLETE"
			valuation.AsOfUnixTime = positionAsOf
			if valuation.AsOfUnixTime < valuation.AnchorUnixTime {
				valuation.AsOfUnixTime = valuation.AnchorUnixTime
			}
			if currencyMismatch {
				// Without a verified FX conversion, preserve the broker statement's
				// base-currency equity instead of adding unlike currencies.
				valuation.PositionValueMinor = statement.PortfolioValueMinor
				valuation.TotalEquityMinor = statement.ClosingNetAssetsMinor
				valuation.Source = "STATEMENT"
				valuation.Quality = "FX_REQUIRED"
			} else {
				valuation.TotalEquityMinor = cashBalance + positionValue
				if costBased {
					valuation.Quality = "COST_BASED"
				}
			}
		} else if currencyMismatch {
			valuation.Quality = "PARTIAL_FX_REQUIRED"
		} else if costBased {
			valuation.Quality = "COST_BASED"
		}
		if valuation.AsOfUnixTime == 0 {
			valuation.AsOfUnixTime = now
		}

		updated, err := datastore.Container.UserDataStore.Query(c, uid).
			Where("uid = ? AND investment_account_id = ?", uid, account.Id).
			Cols("base_currency", "anchor_cash_balance_minor", "anchor_unix_time", "position_value_minor", "total_equity_minor", "source", "quality", "as_of_unix_time", "updated_unix_time").Update(valuation)
		if err != nil {
			return err
		}
		if updated == 0 {
			if _, err := datastore.Container.UserDataStore.Query(c, uid).Insert(valuation); err != nil {
				return err
			}
		}
	}
	return nil
}

// applyInvestmentValuationsToAccountResponses overlays only the account-list
// presentation. The account table and its transaction-derived book balance are
// left untouched, so securities trades never become fake income or expenses.
func applyInvestmentValuationsToAccountResponses(c core.Context, uid int64, responses map[int64]*models.AccountInfoResponse) error {
	linkedAccounts := make([]*hengcai.InvestmentAccount, 0)
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND active = ? AND account_id > 0", uid, true).Find(&linkedAccounts); err != nil {
		return err
	}
	valuations := make([]*hengcai.InvestmentAccountValuation, 0)
	if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Find(&valuations); err != nil {
		return err
	}
	valuationByInvestmentAccount := make(map[int64]*hengcai.InvestmentAccountValuation, len(valuations))
	for _, valuation := range valuations {
		valuationByInvestmentAccount[valuation.InvestmentAccountId] = valuation
	}
	for _, linked := range linkedAccounts {
		response := responses[linked.AccountId]
		valuation := valuationByInvestmentAccount[linked.Id]
		if response == nil || valuation == nil || response.Category != models.ACCOUNT_CATEGORY_INVESTMENT || response.Type != models.ACCOUNT_TYPE_SINGLE_ACCOUNT || !strings.EqualFold(response.Currency, valuation.BaseCurrency) {
			continue
		}
		response.BookBalance = response.Balance
		response.Balance = valuation.TotalEquityMinor
		response.InvestmentValuation = true
		response.InvestmentAccountId = linked.Id
		response.ValuationAsOfUnixTime = valuation.AsOfUnixTime
		response.ValuationSource = valuation.Source
		response.ValuationQuality = valuation.Quality
	}
	return nil
}
