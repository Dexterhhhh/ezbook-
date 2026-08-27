package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"xorm.io/xorm"

	"github.com/mayswind/ezbookkeeping/pkg/core"
	"github.com/mayswind/ezbookkeeping/pkg/datastore"
	"github.com/mayswind/ezbookkeeping/pkg/errs"
	"github.com/mayswind/ezbookkeeping/pkg/hengcai"
	"github.com/mayswind/ezbookkeeping/pkg/hengcai/investmentstatement"
)

const maxInvestmentStatementSize = 20 * 1024 * 1024

func readInvestmentStatementUpload(c *core.WebContext) (investmentstatement.Statement, error) {
	if c.Request == nil {
		return investmentstatement.Statement{}, errors.New("上传请求为空")
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		return investmentstatement.Statement{}, errors.New("请选择券商 PDF 对账单")
	}
	defer file.Close()
	if header.Size <= 0 || header.Size > maxInvestmentStatementSize {
		return investmentstatement.Statement{}, errors.New("对账单必须小于 20 MB")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxInvestmentStatementSize+1))
	if err != nil {
		return investmentstatement.Statement{}, err
	}
	if len(data) > maxInvestmentStatementSize {
		return investmentstatement.Statement{}, errors.New("对账单必须小于 20 MB")
	}
	password := c.PostForm("password")
	engine := c.PostForm("engine")
	return investmentstatement.ParsePDFWithEngine(bytes.NewReader(data), int64(len(data)), password, engine)
}

func (a *HengcaiApi) previewInvestmentStatement(c *core.WebContext) (any, *errs.Error) {
	statement, err := readInvestmentStatementUpload(c)
	if err != nil {
		return nil, hcError(err)
	}
	return map[string]any{"statement": statement, "preview_only": true}, nil
}

func (a *HengcaiApi) listInvestmentReconciliations(c *core.WebContext) (any, *errs.Error) {
	rows := make([]*hengcai.InvestmentStatementImport, 0)
	if err := datastore.Container.UserDataStore.Query(c, c.GetCurrentUid()).Where("uid = ?", c.GetCurrentUid()).Desc("period_end").Desc("id").Find(&rows); err != nil {
		return nil, errs.ErrOperationFailed
	}
	return rows, nil
}

func (a *HengcaiApi) confirmInvestmentStatement(c *core.WebContext) (any, *errs.Error) {
	uid := c.GetCurrentUid()
	accountID, err := strconv.ParseInt(c.PostForm("investment_account_id"), 10, 64)
	if err != nil || accountID <= 0 {
		return nil, hcError(errors.New("请选择需要对账的投资账户"))
	}
	if !strings.EqualFold(c.PostForm("replace_period"), "true") {
		return nil, hcError(errors.New("必须确认用对账单替换该账户在结单周期内的交易"))
	}
	statement, err := readInvestmentStatementUpload(c)
	if err != nil {
		return nil, hcError(err)
	}
	if !statement.Ready {
		return nil, hcError(fmt.Errorf("对账单未通过完整性校验: %s", strings.Join(statement.ValidationErrors, "；")))
	}
	var account hengcai.InvestmentAccount
	if exists, queryErr := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND id = ? AND active = ?", uid, accountID, true).Get(&account); queryErr != nil {
		return nil, errs.ErrOperationFailed
	} else if !exists {
		return nil, hcError(errors.New("投资账户不存在、已停用或不属于当前用户"))
	}
	if !strings.EqualFold(account.BaseCurrency, statement.BaseCurrency) {
		return nil, hcError(fmt.Errorf("投资账户基础币种为 %s，但对账单基础币种为 %s；请选择币种一致的投资账户", account.BaseCurrency, statement.BaseCurrency))
	}
	var duplicate hengcai.InvestmentStatementImport
	if exists, queryErr := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND investment_account_id = ? AND artifact_hash = ? AND status = ?", uid, accountID, statement.ArtifactSHA256, "RECONCILED").Get(&duplicate); queryErr != nil {
		return nil, errs.ErrOperationFailed
	} else if exists {
		return nil, hcError(fmt.Errorf("该对账单已经确认，记录编号为 %d", duplicate.Id))
	}

	periodStart, _ := time.Parse("2006-01-02", statement.PeriodStart)
	periodEnd, _ := time.Parse("2006-01-02", statement.PeriodEnd)
	periodEndExclusive := periodEnd.AddDate(0, 0, 1)
	var saved *hengcai.InvestmentStatementImport
	err = datastore.Container.UserDataStore.DoTransaction(uid, c, func(sess *xorm.Session) error {
		instruments, instrumentErr := upsertStatementInstruments(sess, uid, statement)
		if instrumentErr != nil {
			return instrumentErr
		}
		deleteResult, deleteErr := sess.Where("uid = ? AND investment_account_id = ? AND traded_at >= ? AND traded_at < ?", uid, accountID, periodStart.Unix(), periodEndExclusive.Unix()).Delete(&hengcai.InvestmentTransaction{})
		if deleteErr != nil {
			return deleteErr
		}
		openingAdjustments, adjustmentErr := reconcileOpeningQuantities(sess, uid, accountID, periodStart, statement, instruments)
		if adjustmentErr != nil {
			return adjustmentErr
		}
		for index, trade := range statement.Trades {
			instrument := instruments[trade.Symbol]
			tradedAt, dateErr := time.Parse("2006-01-02", trade.TradeDate)
			if dateErr != nil {
				return dateErr
			}
			quantityDelta := math.Abs(trade.Quantity)
			if trade.Action == "SELL" {
				quantityDelta = -quantityDelta
			}
			reference := trade.ExternalReference
			if reference == "" {
				reference = fmt.Sprintf("%s:%s:%d", statement.ArtifactSHA256[:12], trade.Symbol, index+1)
			}
			row := &hengcai.InvestmentTransaction{Uid: uid, InvestmentAccountId: accountID, InstrumentId: instrument.Id, TradedAt: tradedAt.Unix(), Action: trade.Action, Quantity: math.Abs(trade.Quantity), QuantityDelta: quantityDelta, Price: trade.Price, GrossAmountMinor: trade.GrossAmountMinor, FeesMinor: trade.FeesMinor, TaxesMinor: trade.TaxesMinor, NetCashAmountMinor: trade.NetCashAmountMinor, Currency: trade.Currency, Source: "STMT_" + statement.Provider, ExternalReference: reference, Note: "由投资对账单确认导入"}
			if _, insertErr := sess.Insert(row); insertErr != nil {
				return insertErr
			}
		}
		if priceErr := saveStatementClosingPrices(sess, uid, periodEndExclusive.Add(-time.Second), statement, instruments); priceErr != nil {
			return priceErr
		}
		validationJSON, _ := json.Marshal(statement.ValidationErrors)
		rawJSON, _ := json.Marshal(statement)
		if _, updateErr := sess.Where("uid = ? AND investment_account_id = ? AND period_start = ? AND period_end = ? AND status = ?", uid, accountID, statement.PeriodStart, statement.PeriodEnd, "RECONCILED").Cols("status").Update(&hengcai.InvestmentStatementImport{Status: "SUPERSEDED"}); updateErr != nil {
			return updateErr
		}
		saved = &hengcai.InvestmentStatementImport{Uid: uid, InvestmentAccountId: accountID, Provider: statement.Provider, BrokerAccount: statement.AccountNumber, PeriodStart: statement.PeriodStart, PeriodEnd: statement.PeriodEnd, BaseCurrency: statement.BaseCurrency, ArtifactHash: statement.ArtifactSHA256, Status: "RECONCILED", TradeCount: len(statement.Trades), HoldingCount: len(statement.Holdings), ReplacedTransactionCount: int(deleteResult), OpeningAdjustmentCount: openingAdjustments, OpeningNetAssetsMinor: statement.OpeningNetAssetsMinor, ClosingNetAssetsMinor: statement.ClosingNetAssetsMinor, PortfolioValueMinor: statement.PortfolioValueMinor, DepositsMinor: statement.DepositsMinor, FeesMinor: statement.FeesMinor, RealizedPnlMinor: statement.RealizedPnlMinor, UnrealizedPnlMinor: statement.UnrealizedPnlMinor, TotalPnlMinor: statement.TotalPnlMinor, ReturnBps: statement.TimeWeightedReturnBps, TransactionsValid: statement.TransactionsValid, HoldingsValid: statement.HoldingsValid, CashValid: statement.CashValid, ValidationErrors: string(validationJSON), RawPayload: string(rawJSON), CreatedUnixTime: time.Now().Unix()}
		if _, insertErr := sess.Insert(saved); insertErr != nil {
			return insertErr
		}
		if returnErr := aggregateStatementInvestmentReturn(sess, uid, statement.PeriodEnd[:7]); returnErr != nil {
			return returnErr
		}
		return nil
	})
	if err != nil {
		return nil, hcError(err)
	}
	if err := a.rebuildInvestmentPositions(c, uid); err != nil {
		return nil, errs.ErrOperationFailed
	}
	return map[string]any{"reconciliation": saved, "statement": statement, "message": fmt.Sprintf("%s 对账完成：已按结单替换 %d 笔旧交易并导入 %d 笔交易", statement.Provider, saved.ReplacedTransactionCount, saved.TradeCount)}, nil
}

func aggregateStatementInvestmentReturn(sess *xorm.Session, uid int64, month string) error {
	imports := make([]*hengcai.InvestmentStatementImport, 0)
	if err := sess.Where("uid = ? AND status = ? AND period_end LIKE ?", uid, "RECONCILED", month+"%").Find(&imports); err != nil {
		return err
	}

	row := &hengcai.InvestmentReturn{Uid: uid, Month: month, Quality: "STATEMENT_AGGREGATED", UpdatedUnixTime: time.Now().Unix()}
	var weightedReturnNumerator int64
	var weightedOpeningAssets int64
	for _, imported := range imports {
		row.RealizedPnlMinor += imported.RealizedPnlMinor
		row.UnrealizedPnlMinor += imported.UnrealizedPnlMinor
		row.TotalReturnMinor += imported.TotalPnlMinor
		if imported.OpeningNetAssetsMinor > 0 {
			weightedReturnNumerator += imported.OpeningNetAssetsMinor * int64(imported.ReturnBps)
			weightedOpeningAssets += imported.OpeningNetAssetsMinor
		}
	}
	if weightedOpeningAssets > 0 {
		row.ReturnBps = int(math.Round(float64(weightedReturnNumerator) / float64(weightedOpeningAssets)))
	}

	updated, err := sess.Where("uid = ? AND month = ?", uid, month).Cols("realized_pnl_minor", "unrealized_pnl_minor", "total_return_minor", "return_bps", "quality", "updated_unix_time").Update(row)
	if err != nil {
		return err
	}
	if updated == 0 {
		_, err = sess.Insert(row)
	}
	return err
}

func upsertStatementInstruments(sess *xorm.Session, uid int64, statement investmentstatement.Statement) (map[string]*hengcai.InvestmentInstrument, error) {
	type definition struct {
		name, assetType, currency                    string
		underlyingSymbol, expirationDate, optionType string
		strikePrice                                  float64
		contractMultiplier                           int
	}
	definitions := make(map[string]definition)
	for _, holding := range statement.Holdings {
		definitions[holding.Symbol] = definition{name: holding.Name, assetType: holding.AssetType, currency: holding.Currency, underlyingSymbol: holding.UnderlyingSymbol, expirationDate: holding.ExpirationDate, optionType: holding.OptionType, strikePrice: holding.StrikePrice, contractMultiplier: holding.ContractMultiplier}
	}
	for _, trade := range statement.Trades {
		current := definitions[trade.Symbol]
		if current.name == "" {
			current.name = trade.Name
			current.assetType = trade.AssetType
			current.currency = trade.Currency
		}
		if current.underlyingSymbol == "" {
			current.underlyingSymbol = trade.UnderlyingSymbol
			current.expirationDate = trade.ExpirationDate
			current.optionType = trade.OptionType
			current.strikePrice = trade.StrikePrice
			current.contractMultiplier = trade.ContractMultiplier
		}
		definitions[trade.Symbol] = current
	}
	result := make(map[string]*hengcai.InvestmentInstrument)
	for symbol, value := range definitions {
		var instrument hengcai.InvestmentInstrument
		market := strings.ToUpper(strings.TrimSpace(statement.Market))
		if market == "" {
			market = "US"
		}
		exists, err := sess.Where("uid = ? AND market = ? AND symbol = ?", uid, market, symbol).Get(&instrument)
		if err != nil {
			return nil, err
		}
		if !exists {
			name := strings.TrimSpace(value.name)
			if name == "" {
				name = symbol
			}
			assetType := strings.ToUpper(strings.TrimSpace(value.assetType))
			if assetType == "" {
				assetType = "STOCK"
			}
			currency := strings.ToUpper(strings.TrimSpace(value.currency))
			if currency == "" {
				currency = statement.BaseCurrency
			}
			contractMultiplier := value.contractMultiplier
			if contractMultiplier <= 0 {
				contractMultiplier = 1
			}
			instrument = hengcai.InvestmentInstrument{Uid: uid, AssetType: assetType, Market: market, Symbol: symbol, Name: name, Currency: currency, ContractKey: market + ":" + symbol, UnderlyingSymbol: value.underlyingSymbol, ExpirationDate: value.expirationDate, OptionType: value.optionType, StrikePrice: value.strikePrice, ContractMultiplier: contractMultiplier, PriceScale: 4, QuantityScale: 6, Active: true}
			if _, err := sess.Insert(&instrument); err != nil {
				return nil, err
			}
		} else {
			contractMultiplier := value.contractMultiplier
			if contractMultiplier <= 0 {
				contractMultiplier = 1
			}
			if _, err := sess.Where("uid = ? AND id = ?", uid, instrument.Id).Cols("name", "asset_type", "currency", "contract_key", "underlying_symbol", "expiration_date", "option_type", "strike_price", "contract_multiplier", "active").Update(&hengcai.InvestmentInstrument{Name: value.name, AssetType: value.assetType, Currency: value.currency, ContractKey: market + ":" + symbol, UnderlyingSymbol: value.underlyingSymbol, ExpirationDate: value.expirationDate, OptionType: value.optionType, StrikePrice: value.strikePrice, ContractMultiplier: contractMultiplier, Active: true}); err != nil {
				return nil, err
			}
		}
		result[symbol] = &instrument
	}
	return result, nil
}

func reconcileOpeningQuantities(sess *xorm.Session, uid, accountID int64, periodStart time.Time, statement investmentstatement.Statement, instruments map[string]*hengcai.InvestmentInstrument) (int, error) {
	var priorTransactions []*hengcai.InvestmentTransaction
	if err := sess.Where("uid = ? AND investment_account_id = ? AND traded_at < ?", uid, accountID, periodStart.Unix()).Find(&priorTransactions); err != nil {
		return 0, err
	}
	var allInstruments []*hengcai.InvestmentInstrument
	if err := sess.Where("uid = ?", uid).Find(&allInstruments); err != nil {
		return 0, err
	}
	symbolByID := make(map[int64]string)
	for _, instrument := range allInstruments {
		symbolByID[instrument.Id] = instrument.Symbol
	}
	actual := make(map[string]float64)
	for _, transaction := range priorTransactions {
		actual[symbolByID[transaction.InstrumentId]] += transaction.QuantityDelta
	}
	target := make(map[string]investmentstatement.Holding)
	for _, holding := range statement.Holdings {
		target[holding.Symbol] = holding
	}
	symbols := make(map[string]bool)
	for symbol := range actual {
		symbols[symbol] = true
	}
	for symbol := range target {
		symbols[symbol] = true
	}
	count := 0
	for symbol := range symbols {
		delta := target[symbol].OpeningQuantity - actual[symbol]
		if math.Abs(delta) <= 1e-8 {
			continue
		}
		instrument := instruments[symbol]
		if instrument == nil {
			continue
		}
		price := target[symbol].AverageCost
		if price <= 0 {
			price = target[symbol].ClosingPrice
		}
		action := "BUY"
		if delta < 0 {
			action = "SELL"
		}
		quantity := math.Abs(delta)
		row := &hengcai.InvestmentTransaction{Uid: uid, InvestmentAccountId: accountID, InstrumentId: instrument.Id, TradedAt: periodStart.Add(-time.Second).Unix(), Action: action, Quantity: quantity, QuantityDelta: delta, Price: price, GrossAmountMinor: int64(math.Round(quantity * price * 100)), NetCashAmountMinor: 0, Currency: instrument.Currency, Source: "RECON_OPEN", ExternalReference: fmt.Sprintf("%s:%s", statement.ArtifactSHA256[:12], symbol), Note: "对账期初持仓调整，不计现金流"}
		if _, err := sess.Insert(row); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func saveStatementClosingPrices(sess *xorm.Session, uid int64, asOf time.Time, statement investmentstatement.Statement, instruments map[string]*hengcai.InvestmentInstrument) error {
	for _, holding := range statement.Holdings {
		if holding.ClosingPrice <= 0 || instruments[holding.Symbol] == nil {
			continue
		}
		raw, _ := json.Marshal(holding)
		row := &hengcai.MarketPrice{Uid: uid, InstrumentId: instruments[holding.Symbol].Id, AsOfUnixTime: asOf.Unix(), Provider: "STMT_" + statement.Provider, Feed: "statement_close", Close: holding.ClosingPrice, RawPayload: string(raw)}
		updated, err := sess.Where("uid = ? AND instrument_id = ? AND as_of_unix_time = ?", uid, row.InstrumentId, row.AsOfUnixTime).Cols("provider", "feed", "close", "raw_payload").Update(row)
		if err != nil {
			return err
		}
		if updated == 0 {
			if _, err := sess.Insert(row); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *HengcaiApi) InvestmentReconciliationPreviewHandler(c *core.WebContext) (any, *errs.Error) {
	return a.previewInvestmentStatement(c)
}

func (a *HengcaiApi) InvestmentReconciliationConfirmHandler(c *core.WebContext) (any, *errs.Error) {
	return a.confirmInvestmentStatement(c)
}

func (a *HengcaiApi) InvestmentReconciliationListHandler(c *core.WebContext) (any, *errs.Error) {
	return a.listInvestmentReconciliations(c)
}
