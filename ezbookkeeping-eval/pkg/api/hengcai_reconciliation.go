package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mayswind/ezbookkeeping/pkg/core"
	"github.com/mayswind/ezbookkeeping/pkg/datastore"
	"github.com/mayswind/ezbookkeeping/pkg/errs"
	"github.com/mayswind/ezbookkeeping/pkg/hengcai"
	"github.com/mayswind/ezbookkeeping/pkg/hengcai/statementparser"
	"github.com/mayswind/ezbookkeeping/pkg/log"
	"github.com/mayswind/ezbookkeeping/pkg/models"
	"github.com/mayswind/ezbookkeeping/pkg/services"
	"github.com/mayswind/ezbookkeeping/pkg/utils"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
	"xorm.io/xorm"
)

const (
	maxReconciliationUploadBytes = 20 << 20
	lineKindIncome               = "INCOME"
	// 每批最多交给 AI 分类的流水条数，避免一次性生成过长 JSON 导致上游响应超时
	aiClassifyBatchSize = 30
	// 每批 AI 分类请求的超时时间
	aiClassifyBatchTimeout = 90 * time.Second
	// 每批失败后的最大请求次数（首次 + 重试）
	aiClassifyBatchMaxAttempts = 2
	// 同时进行的 AI 分类请求批次数
	aiClassifyMaxConcurrentBatches = 3
)

type aiClassifySuggestion struct {
	LineHash   string `json:"line_hash"`
	CategoryID int64  `json:"category_id"`
	Confidence int    `json:"confidence_bps"`
}

type aiClassifyBatchOutcome struct {
	suggestions []aiClassifySuggestion
	err         error
}

type normalizedExpenseLine struct {
	Date, Description, Direction, Kind, Currency, ExternalReference, PaymentChannel, MerchantChannel, FundingSource, Raw string
	AmountMinor                                                                                                          int64
	Category                                                                                                             string
}

func normalizeCSVText(data []byte) ([]byte, error) {
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if utf8.Valid(data) {
		return data, nil
	}
	return io.ReadAll(transform.NewReader(bytes.NewReader(data), simplifiedchinese.GB18030.NewDecoder()))
}

func cleanCell(v string) string {
	return strings.TrimSpace(strings.Trim(v, "\ufeff\t\r\n`\""))
}

func firstValue(row map[string]string, names ...string) string {
	for _, name := range names {
		if v := cleanCell(row[name]); v != "" && v != "/" {
			return v
		}
	}
	return ""
}

func inferFundingSource(payment, description string) string {
	v := strings.ToUpper(payment + " " + description)
	if strings.Contains(v, "信用卡") || strings.Contains(v, "CREDIT") || strings.Contains(v, "CARD") || strings.Contains(v, "招商") || strings.Contains(v, "汇丰") || strings.Contains(v, "HSBC") || strings.Contains(v, "IBKR") {
		return "CREDIT_CARD"
	}
	if payment != "" || strings.Contains(v, "银行") || strings.Contains(v, "BANK") {
		return "BANK_ACCOUNT"
	}
	return "UNKNOWN"
}

func parseStatementDate(v string) string {
	v = cleanCell(v)
	for _, layout := range []string{"2006-01-02 15:04:05", "2006/01/02 15:04:05", "2006-01-02", "2006/01/02"} {
		if parsed, err := time.ParseInLocation(layout, v, time.Local); err == nil {
			return parsed.Format("2006-01-02")
		}
	}
	return ""
}

func parseExpenseCSV(provider string, data []byte) ([]normalizedExpenseLine, error) {
	decoded, err := normalizeCSVText(data)
	if err != nil {
		return nil, err
	}
	reader := csv.NewReader(bytes.NewReader(decoded))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("CSV 解析失败: %w", err)
	}
	headerAt := -1
	var headers []string
	for i, record := range records {
		joined := strings.Join(record, "|")
		if strings.Contains(joined, "交易时间") && (strings.Contains(joined, "金额") || strings.Contains(joined, "金额(元)")) {
			headerAt = i
			headers = make([]string, len(record))
			for j := range record {
				headers[j] = cleanCell(record[j])
			}
			break
		}
	}
	if headerAt < 0 {
		return nil, errors.New("未找到支付宝/微信账单表头，请上传官方导出的 CSV")
	}
	lines := make([]normalizedExpenseLine, 0)
	for _, record := range records[headerAt+1:] {
		row := map[string]string{}
		for i, header := range headers {
			if i < len(record) {
				row[header] = cleanCell(record[i])
			}
		}
		date := parseStatementDate(firstValue(row, "交易时间", "创建时间"))
		if date == "" {
			continue
		}
		typeName := firstValue(row, "收/支", "收支类型")
		if typeName != "支出" && typeName != "收入" {
			continue
		}
		status := firstValue(row, "当前状态", "交易状态", "状态")
		if strings.Contains(status, "关闭") || strings.Contains(status, "失败") {
			continue
		}
		amountText := strings.NewReplacer("¥", "", "¥", "", ",", "", " ", "").Replace(firstValue(row, "金额(元)", "金额（元）", "金额"))
		amount, amountErr := utils.ParseAmount(amountText)
		if amountErr != nil || amount == 0 {
			continue
		}
		desc := firstValue(row, "商品说明", "商品", "交易对方", "备注")
		payment := firstValue(row, "收/付款方式", "支付方式", "账户", "银行卡")
		kind, direction := "PURCHASE", "DEBIT"
		if typeName == "收入" {
			kind, direction = lineKindIncome, "CREDIT"
		}
		if strings.Contains(status, "退款") || strings.Contains(desc, "退款") {
			// Refunds calibrate the original expense and are not regular income.
			kind, direction, amount = "REFUND", "CREDIT", -amount
		}
		external := firstValue(row, "交易单号", "交易订单号", "商家订单号")
		category := firstValue(row, "交易分类", "交易类型", "分类")
		raw, _ := json.Marshal(row)
		merchantChannel := strings.ToUpper(provider)
		lines = append(lines, normalizedExpenseLine{Date: date, Description: desc, Direction: direction, Kind: kind, Currency: "CNY", AmountMinor: amount, ExternalReference: external, PaymentChannel: payment, MerchantChannel: merchantChannel, FundingSource: inferFundingSource(payment, desc), Raw: string(raw), Category: category})
	}
	if len(lines) == 0 {
		return nil, errors.New("账单中没有可对账的成功收支流水")
	}
	return lines, nil
}

func statementMonth(statement *hengcai.StatementImport) string {
	if len(statement.PeriodEnd) >= 7 {
		return statement.PeriodEnd[:7]
	}
	return ""
}

func statementOverlapsMonth(statement *hengcai.StatementImport, month string) bool {
	if statement == nil || !validMonth(month) {
		return false
	}
	if statement.PeriodStart == "" || statement.PeriodEnd == "" {
		return statementMonth(statement) == month
	}
	parsedMonth, _ := time.Parse("2006-01", month)
	monthStart := parsedMonth.Format("2006-01-02")
	monthEnd := time.Date(parsedMonth.Year(), parsedMonth.Month()+1, 0, 0, 0, 0, 0, time.Local).Format("2006-01-02")
	return statement.PeriodStart <= monthEnd && statement.PeriodEnd >= monthStart
}

func (a *HengcaiApi) reconciliationUpload(c *core.WebContext) (any, *errs.Error) {
	provider := strings.ToUpper(cleanCell(c.PostForm("provider")))
	accountID, _ := strconv.ParseInt(c.PostForm("account_id"), 10, 64)
	if accountID <= 0 {
		return nil, hcError(errors.New("请选择账单对应的主账本账户"))
	}
	account, err := services.Accounts.GetAccountByAccountId(c, c.GetCurrentUid(), accountID)
	if err != nil || account == nil {
		return nil, hcError(errors.New("主账本账户不存在"))
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		return nil, hcError(errors.New("请选择账单文件"))
	}
	defer file.Close()
	if header.Size > maxReconciliationUploadBytes {
		return nil, hcError(errors.New("账单文件不能超过 20 MB"))
	}
	dataBytes, err := io.ReadAll(io.LimitReader(file, maxReconciliationUploadBytes+1))
	if err != nil || len(dataBytes) > maxReconciliationUploadBytes {
		return nil, hcError(errors.New("账单读取失败或超过 20 MB"))
	}
	hash := fmt.Sprintf("%x", sha256Bytes(dataBytes))
	uid := c.GetCurrentUid()
	var duplicate hengcai.StatementImport
	if ok, _ := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND artifact_hash = ?", uid, hash).Get(&duplicate); ok {
		return nil, hcError(fmt.Errorf("该文件已导入（记录 #%d），为避免重复入账已拒绝", duplicate.Id))
	}
	periodType, coverageDimension := "CALENDAR_MONTH", "MERCHANT_CHANNEL"
	if provider == "CMB" {
		periodType, coverageDimension = "BILLING_CYCLE", "FUNDING_SOURCE"
	}
	if provider == statementparser.DebitStatementProvider {
		periodType, coverageDimension = "CALENDAR_MONTH", "FUNDING_SOURCE"
	}
	statement := &hengcai.StatementImport{Uid: uid, Provider: provider, AccountId: accountID, Currency: account.Currency, ArtifactHash: hash, Status: "REVIEW", BalanceValid: true, SummaryValid: true, ValidationErrors: "[]", CreatedUnixTime: time.Now().Unix(), FileName: header.Filename, DisplayName: provider + " " + header.Filename, PeriodType: periodType, CoverageDimension: coverageDimension, CoverageStatus: "PENDING", Revision: 1}
	var lines []*hengcai.StatementLine
	if provider == "CMB" {
		parsed, parseErr := statementparser.ParseCMBCreditCardPDF(bytes.NewReader(dataBytes), int64(len(dataBytes)))
		if parseErr != nil {
			return nil, hcError(parseErr)
		}
		statement.StatementDate, statement.PeriodStart, statement.PeriodEnd = parsed.StatementDate, parsed.StatementPeriodStart, parsed.StatementPeriodEnd
		statement.BillingDate = parsed.StatementDate
		statement.CoveredUntil = parsed.StatementPeriodEnd
		statement.Currency, statement.OpeningBalanceMinor, statement.ClosingBalanceMinor = parsed.Currency, parsed.OpeningBalanceMinor, parsed.ClosingBalanceMinor
		statement.TotalDebitMinor, statement.TotalCreditMinor = parsed.TotalDebitMinor, parsed.TotalCreditMinor
		statement.BalanceValid, statement.SummaryValid = parsed.BalanceValid, parsed.SummaryValid
		validation, _ := json.Marshal(parsed.ValidationErrors)
		statement.ValidationErrors = string(validation)
		raw, _ := json.Marshal(parsed)
		statement.RawPayload = string(raw)
		for _, item := range parsed.Lines {
			date := item.PostedDate
			if item.TransactionDate != nil {
				date = *item.TransactionDate
			}
			lineRaw, _ := json.Marshal(item)
			merchantChannel := platformChannel(item.Description)
			lines = append(lines, &hengcai.StatementLine{Uid: uid, LineNumber: item.LineNumber, TransactionDate: date, PostedDate: item.PostedDate, Description: item.Description, AmountMinor: item.AmountMinor, SignedAmountMinor: item.SignedAmountMinor, Direction: item.Direction, Currency: parsed.Currency, CardLastFour: item.CardLastFour, Section: item.Section, LineKind: item.LineKind, AccountingTreatment: item.AccountingTreatment, SettlesPriorStatement: item.SettlesPriorStatement, RequiresReview: item.RequiresExpenseReview, LineHash: item.LineHash, Status: "UNMATCHED", RawPayload: string(lineRaw), MerchantChannel: merchantChannel, FundingSource: "CREDIT_CARD", EntrySource: "CREDIT_CARD_STATEMENT", CoverageState: "PROVISIONAL"})
		}
	} else if provider == statementparser.DebitStatementProvider {
		parsed, parseErr := statementparser.ParseCMBSavingsPDF(bytes.NewReader(dataBytes), int64(len(dataBytes)))
		if parseErr != nil {
			return nil, hcError(parseErr)
		}
		statement.StatementDate, statement.PeriodStart, statement.PeriodEnd = parsed.StatementDate, parsed.StatementPeriodStart, parsed.StatementPeriodEnd
		statement.CoveredUntil = parsed.StatementPeriodEnd
		statement.Currency, statement.OpeningBalanceMinor, statement.ClosingBalanceMinor = parsed.Currency, parsed.OpeningBalanceMinor, parsed.ClosingBalanceMinor
		statement.TotalDebitMinor, statement.TotalCreditMinor = parsed.TotalDebitMinor, parsed.TotalCreditMinor
		statement.BalanceValid, statement.SummaryValid = parsed.BalanceValid, parsed.SummaryValid
		validation, _ := json.Marshal(parsed.ValidationErrors)
		statement.ValidationErrors = string(validation)
		raw, _ := json.Marshal(parsed)
		statement.RawPayload = string(raw)
		for _, item := range parsed.Lines {
			lineRaw, _ := json.Marshal(item)
			status := "UNMATCHED"
			if item.RequiresManualReview {
				status = "REVIEW"
			}
			lines = append(lines, &hengcai.StatementLine{Uid: uid, LineNumber: item.LineNumber, TransactionDate: item.PostedDate, PostedDate: item.PostedDate, Description: item.Description, Counterparty: item.Counterparty, CounterpartyType: item.CounterpartyType, ReviewReason: item.ReviewReason, AmountMinor: item.AmountMinor, SignedAmountMinor: item.SignedAmountMinor, Direction: item.Direction, Currency: item.Currency, LineKind: item.LineKind, AccountingTreatment: item.AccountingTreatment, RequiresReview: item.RequiresManualReview, LineHash: item.LineHash, Status: status, RawPayload: string(lineRaw), PaymentChannel: item.PaymentChannel, MerchantChannel: item.MerchantChannel, FundingSource: item.FundingSource, EntrySource: "CMB_SAVINGS_STATEMENT", CoverageState: "PROVISIONAL"})
		}
	} else if provider == "ALIPAY" || provider == "WECHAT" {
		normalized, parseErr := parseExpenseCSV(provider, dataBytes)
		if parseErr != nil {
			return nil, hcError(parseErr)
		}
		sort.Slice(normalized, func(i, j int) bool { return normalized[i].Date < normalized[j].Date })
		statement.PeriodStart, statement.PeriodEnd, statement.StatementDate = normalized[0].Date, normalized[len(normalized)-1].Date, normalized[len(normalized)-1].Date
		statement.CoveredUntil = statement.PeriodEnd
		statement.RawPayload = fmt.Sprintf(`{"normalized_line_count":%d}`, len(normalized))
		for i, item := range normalized {
			lineHash := fmt.Sprintf("%x", sha256Bytes([]byte(provider+"|"+item.Date+"|"+item.ExternalReference+"|"+strconv.FormatInt(item.AmountMinor, 10)+"|"+item.Description)))
			lines = append(lines, &hengcai.StatementLine{Uid: uid, LineNumber: i + 1, TransactionDate: item.Date, PostedDate: item.Date, Description: item.Description, AmountMinor: item.AmountMinor, SignedAmountMinor: item.AmountMinor, Direction: item.Direction, Currency: item.Currency, LineKind: item.Kind, AccountingTreatment: "EXPENSE_LEDGER", LineHash: lineHash, Status: "UNMATCHED", RawPayload: item.Raw, ExternalReference: item.ExternalReference, PaymentChannel: item.PaymentChannel, MerchantChannel: item.MerchantChannel, FundingSource: item.FundingSource, EntrySource: provider + "_STATEMENT", CoverageState: "PROVISIONAL", StatementCategory: item.Category})
			if item.Kind == "PURCHASE" {
				statement.TotalDebitMinor += item.AmountMinor
			} else {
				statement.TotalCreditMinor += item.AmountMinor
			}
		}
	} else {
		return nil, hcError(errors.New("目前可选识别引擎为：支付宝、微信支付、招商银行信用卡、招商银行储蓄卡"))
	}
	err = datastore.Container.UserDataStore.DoTransaction(uid, c, func(sess *xorm.Session) error {
		if _, insertErr := sess.Insert(statement); insertErr != nil {
			return insertErr
		}
		for _, line := range lines {
			line.StatementId = statement.Id
			if _, insertErr := sess.Insert(line); insertErr != nil {
				return insertErr
			}
		}
		return nil
	})
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	_ = a.upsertTransactionCoverage(c, statement)
	_ = a.buildReconciliationMatches(c, statement, lines)
	return map[string]any{"statement": statement, "line_count": len(lines), "month": statementMonth(statement)}, nil
}

func (a *HengcaiApi) upsertTransactionCoverage(c *core.WebContext, statement *hengcai.StatementImport) error {
	if statement == nil || statement.PeriodStart == "" || statement.PeriodEnd == "" {
		return nil
	}
	dimension := statement.CoverageDimension
	if dimension == "" {
		dimension = "MERCHANT_CHANNEL"
	}
	var coverage hengcai.TransactionCoverage
	uid := c.GetCurrentUid()
	has, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND dimension = ? AND source = ? AND account_id = ?", uid, dimension, statement.Provider, statement.AccountId).Get(&coverage)
	if err != nil {
		return err
	}
	if has {
		if statement.PeriodEnd < coverage.CoveredUntil {
			return nil // Older cycles cannot move the rolling watermark backwards.
		}
		coverage.CoverageStart, coverage.CoverageEnd, coverage.CoveredUntil = statement.PeriodStart, statement.PeriodEnd, statement.PeriodEnd
		coverage.StatementId, coverage.Revision, coverage.Status, coverage.UpdatedUnixTime = statement.Id, coverage.Revision+1, "PENDING", time.Now().Unix()
		_, err = datastore.Container.UserDataStore.Query(c, uid).ID(coverage.Id).Cols("coverage_start", "coverage_end", "covered_until", "statement_id", "revision", "status", "updated_unix_time").Update(&coverage)
		return err
	}
	coverage = hengcai.TransactionCoverage{Uid: uid, Dimension: dimension, Source: statement.Provider, AccountId: statement.AccountId, PeriodType: statement.PeriodType, CoverageStart: statement.PeriodStart, CoverageEnd: statement.PeriodEnd, CoveredUntil: statement.PeriodEnd, StatementId: statement.Id, Revision: 1, Status: "PENDING", UpdatedUnixTime: time.Now().Unix()}
	_, err = datastore.Container.UserDataStore.Query(c, uid).Insert(&coverage)
	return err
}

func sha256Bytes(data []byte) [32]byte { return sha256.Sum256(data) }

func platformChannel(description string) string {
	v := strings.ToUpper(description)
	if strings.Contains(v, "支付宝") || strings.Contains(v, "ALIPAY") {
		return "ALIPAY"
	}
	if strings.Contains(v, "微信") || strings.Contains(v, "财付通") || strings.Contains(v, "WECHAT") || strings.Contains(v, "TENPAY") {
		return "WECHAT"
	}
	return ""
}

func isCmbSavingsPlatformSettlement(description string) bool {
	description = strings.TrimSpace(description)
	return description == "快捷支付" || description == "快捷退款"
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func isReconciliationLineFinal(status string) bool {
	return status == "EVIDENCE" || status == "POSTED" || status == "MATCHED"
}

func platformCoverageDate(line *hengcai.StatementLine) string {
	if line == nil {
		return ""
	}
	if _, err := time.Parse("2006-01-02", line.TransactionDate); err == nil {
		return line.TransactionDate
	}
	return line.PostedDate
}

func hasVerifiedPlatformCoverage(statements []hengcai.StatementImport, channel string, date string) bool {
	if channel == "" || date == "" {
		return false
	}
	for i := range statements {
		statement := &statements[i]
		if statement.Provider != channel || statement.CoverageDimension != "MERCHANT_CHANNEL" {
			continue
		}
		if statement.Status != "POSTED" && statement.CoverageStatus != "VERIFIED" {
			continue
		}
		if statement.PeriodStart <= date && date <= statement.PeriodEnd {
			return true
		}
	}
	return false
}

func shouldRestoreUnionPayReview(line *hengcai.StatementLine) bool {
	if line == nil || isReconciliationLineFinal(line.Status) || line.RequiresReview {
		return false
	}
	if line.CounterpartyType != "PERSON" || strings.TrimSpace(line.Description) != "银联快捷支付" {
		return false
	}
	if line.LineKind != statementparser.LineKindPurchase && line.LineKind != lineKindIncome {
		return false
	}
	return line.Classification != "人工确认" && line.MatchType != "MANUAL_CLASSIFICATION"
}

func shouldRepairManualClassification(line *hengcai.StatementLine) bool {
	if line == nil || isReconciliationLineFinal(line.Status) || line.CounterpartyType != "PERSON" {
		return false
	}
	manualDecision := line.Classification == "人工确认" || (line.LineKind == statementparser.LineKindRefund && line.Classification == "人工确认退款")
	if !manualDecision || line.CategoryId <= 0 || line.MatchType == "PLATFORM_UNRESOLVED" {
		return false
	}
	return line.LineKind == statementparser.LineKindPurchase || line.LineKind == statementparser.LineKindRefund || line.LineKind == lineKindIncome
}

func (a *HengcaiApi) buildReconciliationMatches(c *core.WebContext, statement *hengcai.StatementImport, lines []*hengcai.StatementLine) error {
	uid, month := c.GetCurrentUid(), statementMonth(statement)
	if !validMonth(month) {
		return nil
	}
	parsedMonth, _ := time.Parse("2006-01", month)
	transactions, _ := services.Transactions.GetTransactionsInMonthByPage(c, uid, int32(parsedMonth.Year()), int32(parsedMonth.Month()), 0, nil, nil, nil, false, "", "", core.MATCH_MODE_DEFAULT, false)
	markers := make([]*hengcai.ManualTransactionMarker, 0)
	_ = datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Find(&markers)
	markerByTransaction := make(map[int64]*hengcai.ManualTransactionMarker, len(markers))
	for _, marker := range markers {
		markerByTransaction[marker.TransactionId] = marker
	}
	allPlatform := make([]hengcai.StatementLine, 0)
	_ = datastore.Container.UserDataStore.Query(c, uid).Table("hengcai_statement_line").Join("INNER", "hengcai_statement_import", "hengcai_statement_line.statement_id = hengcai_statement_import.id").Where("hengcai_statement_line.uid = ? AND hengcai_statement_import.provider IN (?, ?)", uid, "ALIPAY", "WECHAT").Find(&allPlatform)
	platformStatements := make([]hengcai.StatementImport, 0)
	_ = datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND provider IN (?, ?)", uid, "ALIPAY", "WECHAT").Find(&platformStatements)
	isBankStatement := statement.Provider == "CMB" || statement.Provider == statementparser.DebitStatementProvider
	settlementFunding, settlementEntrySource := "CREDIT_CARD", "CREDIT_CARD_STATEMENT"
	if statement.Provider == statementparser.DebitStatementProvider {
		settlementFunding, settlementEntrySource = "BANK_ACCOUNT", "CMB_SAVINGS_STATEMENT"
	}
	now := time.Now().Unix()
	for _, line := range lines {
		if line.Status == "EVIDENCE" || line.Status == "POSTED" {
			// Finalized evidence/ledger rows must never keep an obsolete review
			// gate. Normalize historical rows without changing whether they enter
			// the ledger.
			columns := make([]string, 0, 5)
			if line.RequiresReview || line.ReviewReason != "" {
				line.RequiresReview, line.ReviewReason = false, ""
				columns = append(columns, "requires_review", "review_reason")
			}
			if line.MatchType == "PERSON_COUNTERPARTY" || line.MatchType == "AMBIGUOUS" {
				if line.Status == "EVIDENCE" {
					line.MatchType = "MANUAL_REVIEW"
				} else {
					line.MatchType = "MANUAL_CLASSIFICATION"
				}
				line.CoverageState = "VERIFIED"
				columns = append(columns, "match_type", "coverage_state")
			} else if line.MatchType == "PLATFORM_UNRESOLVED" {
				if line.Status == "EVIDENCE" {
					line.MatchType = "MANUAL_PLATFORM_EVIDENCE"
				} else {
					line.MatchType = "MANUAL_PLATFORM_LEDGER"
				}
				line.CoverageState = "VERIFIED"
				columns = append(columns, "match_type", "coverage_state")
			}
			if len(columns) > 0 {
				_, _ = datastore.Container.UserDataStore.Query(c, uid).ID(line.Id).Cols(columns...).Update(line)
			}
			continue
		}
		if line.Status == "CLASSIFIED" && line.MatchType == "MANUAL_PLATFORM_LEDGER" {
			// The user has explicitly decided that this platform-looking bank row
			// is an independent ledger transaction. Do not run duplicate matching
			// again and overwrite that auditable decision.
			continue
		}
		if statement.Provider == statementparser.DebitStatementProvider && shouldRestoreUnionPayReview(line) {
			// 银联快捷支付 is intentionally outside the new exclusion rule
			// until the user confirms its source. Explicit manual classifications
			// and refunds are final user decisions and are deliberately excluded.
			line.RequiresReview, line.Status, line.MatchType, line.CoverageState, line.ReviewReason = true, "REVIEW", "PERSON_COUNTERPARTY", "CONFLICT", "个人对手方必须人工确认；银联快捷支付暂不按平台结算剔除"
			_, _ = datastore.Container.UserDataStore.Query(c, uid).ID(line.Id).Cols("requires_review", "status", "match_type", "coverage_state", "review_reason").Update(line)
			continue
		}
		if statement.Provider == statementparser.DebitStatementProvider && line.Status != "POSTED" && isCmbSavingsPlatformSettlement(line.Description) {
			// CMB savings-account shortcut payment/refund rows are settlement
			// traces of Alipay/WeChat. The platform statement is the canonical
			// ledger source, so exclude the bank row directly and never match or
			// post it as another transaction.
			line.Status, line.MatchType, line.RequiresReview, line.ReviewReason = "EVIDENCE", "PLATFORM_SETTLEMENT_EXCLUDED", false, ""
			line.MatchedTransactionId, line.MatchScoreBps = 0, 0
			line.MerchantChannel, line.FundingSource, line.EntrySource, line.CoverageState = "PLATFORM", "BANK_ACCOUNT", "CMB_SAVINGS_STATEMENT", "VERIFIED"
			line.Classification = "支付宝/微信已覆盖，银行快捷流水剔除"
			_, _ = datastore.Container.UserDataStore.Query(c, uid).ID(line.Id).Cols("status", "match_type", "requires_review", "review_reason", "matched_transaction_id", "match_score_bps", "merchant_channel", "funding_source", "entry_source", "coverage_state", "classification").Update(line)
			continue
		}
		if statement.Provider == statementparser.DebitStatementProvider && line.CounterpartyType == "PERSON" && isCmbSavingsPlatformSettlement(line.Description) && line.RequiresReview {
			// These bank-provided labels are deterministic settlement/payment
			// flows. Repair rows imported before this rule existed so a refresh
			// does not leave them stuck behind the old person-counterparty gate.
			line.RequiresReview, line.ReviewReason = false, ""
			if line.Status == "REVIEW" && line.MatchType == "PERSON_COUNTERPARTY" {
				line.Status, line.MatchType, line.CoverageState = "UNMATCHED", "", "PROVISIONAL"
			}
			_, _ = datastore.Container.UserDataStore.Query(c, uid).ID(line.Id).Cols("requires_review", "review_reason", "status", "match_type", "coverage_state").Update(line)
		}
		if shouldRepairManualClassification(line) {
			// Repair rows saved by the previous UI/backend combination: the
			// explicit category choice is already a manual decision, but the old
			// review gate was left behind and made the row block posting forever.
			line.RequiresReview, line.ReviewReason = false, ""
			line.Status, line.MatchType, line.CoverageState = "CLASSIFIED", "MANUAL_CLASSIFICATION", "VERIFIED"
			_, _ = datastore.Container.UserDataStore.Query(c, uid).ID(line.Id).Cols("requires_review", "review_reason", "status", "match_type", "coverage_state").Update(line)
			continue
		}
		if isReconciliationLineFinal(line.Status) {
			continue
		}
		if line.CounterpartyType == "PERSON" && line.RequiresReview {
			line.Status, line.MatchType, line.CoverageState = "REVIEW", "PERSON_COUNTERPARTY", "CONFLICT"
			line.ReviewReason = "个人对手方必须人工确认，禁止自动匹配或重复入账"
			_, _ = datastore.Container.UserDataStore.Query(c, uid).ID(line.Id).Cols("status", "match_type", "coverage_state", "review_reason").Update(line)
			continue
		}
		channel := platformChannel(line.Description + " " + line.Counterparty)
		if isBankStatement && channel != "" {
			var candidate *hengcai.StatementLine
			platformCandidateCount := 0
			for i := range allPlatform {
				other := &allPlatform[i]
				if other.MerchantChannel == channel && abs64(other.AmountMinor) == abs64(line.AmountMinor) && dateDistanceDays(other.PostedDate, line.PostedDate) <= 3 {
					platformCandidateCount++
					if candidate != nil {
						candidate = nil
						break
					}
					candidate = other
				}
			}
			if candidate != nil {
				line.Status, line.MatchType, line.PaymentChannel, line.MerchantChannel, line.FundingSource, line.EntrySource, line.CoverageState, line.MatchScoreBps = "EVIDENCE", "SETTLEMENT_EVIDENCE", channel, channel, settlementFunding, settlementEntrySource, "VERIFIED", 10000
				_, _ = datastore.Container.UserDataStore.Query(c, uid).ID(line.Id).Cols("status", "match_type", "payment_channel", "merchant_channel", "funding_source", "entry_source", "coverage_state", "match_score_bps").Update(line)
				_, _ = datastore.Container.UserDataStore.Query(c, uid).Insert(&hengcai.ReconciliationMatch{Uid: uid, Month: month, StatementLineId: line.Id, RelatedStatementLineId: candidate.Id, MatchType: "SETTLEMENT_EVIDENCE", ScoreBps: 10000, Status: "CONFIRMED", Reason: "银行流水与支付宝/微信明细金额及日期一致，保留为结算证据，不重复入账", CreatedUnixTime: now})
				if candidate.MatchedTransactionId > 0 {
					_, _ = datastore.Container.UserDataStore.Query(c, uid).Insert(&hengcai.TransactionEvidence{Uid: uid, TransactionId: candidate.MatchedTransactionId, StatementLineId: line.Id, EvidenceType: "SETTLEMENT", MerchantChannel: channel, FundingSource: settlementFunding, VerificationState: "VERIFIED", MatchScoreBps: 10000, CreatedUnixTime: now})
					_ = a.verifyManualMarkerDimension(c, candidate.MatchedTransactionId, "FUNDING_SOURCE")
				}
				continue
			}
			if hasVerifiedPlatformCoverage(platformStatements, channel, platformCoverageDate(line)) {
				// A verified platform statement is the canonical ledger source for its
				// covered period. Repeated equal-value purchases and refunds are not
				// always uniquely identifiable in platform exports, so lack of a unique
				// row-level counterpart must not force the user to exclude each bank
				// settlement manually.
				line.Status, line.MatchType, line.PaymentChannel, line.MerchantChannel = "EVIDENCE", "PLATFORM_COVERAGE_EVIDENCE", channel, channel
				line.FundingSource, line.EntrySource, line.CoverageState, line.MatchScoreBps = settlementFunding, settlementEntrySource, "VERIFIED", 9000
				line.RequiresReview, line.ReviewReason = false, ""
				line.MatchedTransactionId = 0
				line.Classification = "支付宝/微信账单已覆盖，银行流水作为结算证据剔除"
				_, _ = datastore.Container.UserDataStore.Query(c, uid).ID(line.Id).Cols("status", "match_type", "payment_channel", "merchant_channel", "funding_source", "entry_source", "coverage_state", "match_score_bps", "requires_review", "review_reason", "matched_transaction_id", "classification").Update(line)
				_, _ = datastore.Container.UserDataStore.Query(c, uid).Insert(&hengcai.ReconciliationMatch{Uid: uid, Month: month, StatementLineId: line.Id, MatchType: "PLATFORM_COVERAGE_EVIDENCE", ScoreBps: 9000, Status: "CONFIRMED", Reason: "交易日已由入账的支付宝/微信账单覆盖，银行流水仅作为结算证据，不重复入账", CreatedUnixTime: now})
				continue
			}
			if platformCandidateCount == 0 && statement.Provider == statementparser.DebitStatementProvider && isCmbSavingsPlatformSettlement(line.Description) {
				// The bank's explicit shortcut label is enough to classify this
				// as an ordinary bank-account line when no platform detail exists.
				// Keep it unmatched for category selection, but do not force a
				// manual counterpart review or block it as a duplicate conflict.
				line.Status, line.MatchType, line.CoverageState, line.ReviewReason = "UNMATCHED", "BANK_SHORTCUT_UNMATCHED", "PROVISIONAL", ""
				_, _ = datastore.Container.UserDataStore.Query(c, uid).ID(line.Id).Cols("status", "match_type", "coverage_state", "review_reason").Update(line)
				continue
			}
			// A platform-looking bank line without one unambiguous platform
			// counterpart must be reviewed; posting it automatically could double
			// count a missing or differently dated Alipay/WeChat detail.
			line.Status, line.MatchType, line.PaymentChannel, line.MerchantChannel, line.FundingSource, line.EntrySource, line.CoverageState, line.ReviewReason = "REVIEW", "PLATFORM_UNRESOLVED", channel, channel, settlementFunding, settlementEntrySource, "CONFLICT", "银行流水对应支付平台明细不存在唯一匹配，禁止重复入账"
			_, _ = datastore.Container.UserDataStore.Query(c, uid).ID(line.Id).Cols("status", "match_type", "payment_channel", "merchant_channel", "funding_source", "entry_source", "coverage_state", "review_reason").Update(line)
			continue
		}
		candidates := make([]*models.Transaction, 0)
		for _, tx := range transactions {
			if tx.AccountId != statement.AccountId || abs64(tx.Amount) != abs64(line.AmountMinor) {
				continue
			}
			if marker := markerByTransaction[tx.TransactionId]; marker != nil {
				if statement.CoverageDimension == "MERCHANT_CHANNEL" && marker.MerchantChannel != "" && marker.MerchantChannel != statement.Provider {
					continue
				}
				if statement.CoverageDimension == "FUNDING_SOURCE" && marker.FundingSource != "" && marker.FundingSource != settlementFunding {
					continue
				}
			}
			txDate := time.Unix(utils.GetUnixTimeFromTransactionTime(tx.TransactionTime), 0).Format("2006-01-02")
			if dateDistanceDays(txDate, line.PostedDate) <= 2 {
				candidates = append(candidates, tx)
			}
		}
		if len(candidates) == 1 {
			line.Status, line.MatchType, line.MatchedTransactionId, line.MatchScoreBps, line.CoverageState = "MATCHED", "MANUAL_MATCH", candidates[0].TransactionId, 9500, "VERIFIED"
			_, _ = datastore.Container.UserDataStore.Query(c, uid).ID(line.Id).Cols("status", "match_type", "matched_transaction_id", "match_score_bps", "coverage_state").Update(line)
			_, _ = datastore.Container.UserDataStore.Query(c, uid).Insert(&hengcai.ReconciliationMatch{Uid: uid, Month: month, StatementLineId: line.Id, TransactionId: candidates[0].TransactionId, MatchType: "MANUAL_MATCH", ScoreBps: 9500, Status: "SUGGESTED", Reason: "同账户、同金额、日期相差不超过 2 天", CreatedUnixTime: now})
			_, _ = datastore.Container.UserDataStore.Query(c, uid).Insert(&hengcai.TransactionEvidence{Uid: uid, TransactionId: candidates[0].TransactionId, StatementLineId: line.Id, EvidenceType: "MERCHANT", MerchantChannel: line.MerchantChannel, FundingSource: line.FundingSource, VerificationState: "VERIFIED", MatchScoreBps: 9500, CreatedUnixTime: now})
			_ = a.verifyManualMarkerDimension(c, candidates[0].TransactionId, statement.CoverageDimension)
		} else if len(candidates) > 1 {
			line.Status, line.MatchType, line.CoverageState = "REVIEW", "AMBIGUOUS", "CONFLICT"
			_, _ = datastore.Container.UserDataStore.Query(c, uid).ID(line.Id).Cols("status", "match_type", "coverage_state").Update(line)
		}
	}
	// Re-run settlement matching when a platform statement is uploaded after
	// the bank statement. This makes import order irrelevant and still keeps
	// the bank row as evidence instead of creating a duplicate core transaction.
	if statement.Provider == "ALIPAY" || statement.Provider == "WECHAT" {
		bankStatements := make([]*hengcai.StatementImport, 0)
		if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND provider IN (?, ?)", uid, "CMB", statementparser.DebitStatementProvider).Find(&bankStatements); err == nil {
			for _, bankStatement := range bankStatements {
				if statementMonth(bankStatement) != month {
					continue
				}
				bankLines := make([]*hengcai.StatementLine, 0)
				if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND statement_id = ?", uid, bankStatement.Id).Find(&bankLines); err == nil && len(bankLines) > 0 {
					_ = a.buildReconciliationMatches(c, bankStatement, bankLines)
				}
			}
		}
	}
	return nil
}

type statementLineTransactionCandidate struct {
	TransactionId   int64  `json:"transaction_id,string"`
	TransactionDate string `json:"transaction_date"`
	AmountMinor     int64  `json:"amount_minor"`
	Type            int    `json:"type"`
	Comment         string `json:"comment"`
}

func (a *HengcaiApi) loadStatementLine(c *core.WebContext, lineID int64) (*hengcai.StatementLine, *hengcai.StatementImport, error) {
	uid := c.GetCurrentUid()
	line := &hengcai.StatementLine{}
	if ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND id = ?", uid, lineID).Get(line); err != nil {
		return nil, nil, err
	} else if !ok {
		return nil, nil, errors.New("账单流水不存在")
	}
	statement := &hengcai.StatementImport{}
	if ok, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND id = ?", uid, line.StatementId).Get(statement); err != nil {
		return nil, nil, err
	} else if !ok {
		return nil, nil, errors.New("账单来源不存在")
	}
	return line, statement, nil
}

func (a *HengcaiApi) findStatementLineCandidates(c *core.WebContext) (any, *errs.Error) {
	lineID, _ := strconv.ParseInt(c.Query("line_id"), 10, 64)
	if lineID <= 0 {
		return nil, hcError(errors.New("line_id 无效"))
	}
	line, statement, err := a.loadStatementLine(c, lineID)
	if err != nil {
		return nil, hcError(err)
	}
	uid := c.GetCurrentUid()
	month := statementMonth(statement)
	parsedMonth, parseErr := time.Parse("2006-01", month)
	if parseErr != nil {
		return nil, hcError(errors.New("账单月份无效"))
	}
	transactions, err := services.Transactions.GetTransactionsInMonthByPage(c, uid, int32(parsedMonth.Year()), int32(parsedMonth.Month()), 0, nil, nil, nil, false, "", "", core.MATCH_MODE_DEFAULT, false)
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	candidates := make([]statementLineTransactionCandidate, 0)
	for _, transaction := range transactions {
		if transaction.AccountId != statement.AccountId || abs64(transaction.Amount) != abs64(line.AmountMinor) {
			continue
		}
		transactionDate := time.Unix(utils.GetUnixTimeFromTransactionTime(transaction.TransactionTime), 0).Format("2006-01-02")
		if dateDistanceDays(transactionDate, line.PostedDate) > 7 {
			continue
		}
		candidates = append(candidates, statementLineTransactionCandidate{TransactionId: transaction.TransactionId, TransactionDate: transactionDate, AmountMinor: transaction.Amount, Type: int(transaction.Type), Comment: transaction.Comment})
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].TransactionDate < candidates[j].TransactionDate })
	return candidates, nil
}

func (a *HengcaiApi) manuallyMatchStatementLine(c *core.WebContext) (any, *errs.Error) {
	lineID, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var input struct {
		TransactionId int64 `json:"transaction_id,string"`
	}
	if lineID <= 0 || c.ShouldBindJSON(&input) != nil || input.TransactionId <= 0 {
		return nil, hcError(errors.New("人工匹配请求无效"))
	}
	line, statement, err := a.loadStatementLine(c, lineID)
	if err != nil {
		return nil, hcError(err)
	}
	uid := c.GetCurrentUid()
	transaction := &models.Transaction{}
	if ok, queryErr := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND transaction_id = ? AND deleted = ?", uid, input.TransactionId, false).Get(transaction); queryErr != nil || !ok {
		return nil, hcError(errors.New("核心交易不存在"))
	}
	transactionDate := time.Unix(utils.GetUnixTimeFromTransactionTime(transaction.TransactionTime), 0).Format("2006-01-02")
	if transaction.AccountId != statement.AccountId || abs64(transaction.Amount) != abs64(line.AmountMinor) || dateDistanceDays(transactionDate, line.PostedDate) > 7 {
		return nil, hcError(errors.New("只能匹配同一账户、金额一致且日期相差不超过 7 天的交易"))
	}
	now := time.Now().Unix()
	ledgerLine := line.LineKind == statementparser.LineKindPurchase || line.LineKind == statementparser.LineKindRefund || line.LineKind == lineKindIncome
	status, matchType, coverageState := "EVIDENCE", "MANUAL_REVIEW", "VERIFIED"
	if ledgerLine {
		status, matchType = "MATCHED", "MANUAL_CONFIRMED"
	}
	line.Status, line.MatchType, line.MatchedTransactionId, line.MatchScoreBps, line.CoverageState, line.ReviewReason = status, matchType, transaction.TransactionId, 10000, coverageState, ""
	cols := []string{"status", "match_type", "matched_transaction_id", "match_score_bps", "coverage_state", "review_reason"}
	if _, err = datastore.Container.UserDataStore.Query(c, uid).ID(line.Id).Where("uid = ?", uid).Cols(cols...).Update(line); err != nil {
		return nil, errs.ErrOperationFailed
	}
	_, _ = datastore.Container.UserDataStore.Query(c, uid).Insert(&hengcai.ReconciliationMatch{Uid: uid, Month: statementMonth(statement), StatementLineId: line.Id, TransactionId: transaction.TransactionId, MatchType: matchType, ScoreBps: 10000, Status: "CONFIRMED", Reason: "人工确认与主账本已有交易一致，保留原交易 ID", CreatedUnixTime: now})
	if !ledgerLine {
		_, _ = datastore.Container.UserDataStore.Query(c, uid).Insert(&hengcai.TransactionEvidence{Uid: uid, TransactionId: transaction.TransactionId, StatementLineId: line.Id, EvidenceType: "MANUAL", MerchantChannel: line.MerchantChannel, FundingSource: line.FundingSource, VerificationState: "VERIFIED", MatchScoreBps: 10000, CreatedUnixTime: now})
	}
	_ = a.verifyManualMarkerDimension(c, transaction.TransactionId, statement.CoverageDimension)
	return line, nil
}

func (a *HengcaiApi) manuallyReviewStatementLine(c *core.WebContext) (any, *errs.Error) {
	lineID, _ := strconv.ParseInt(c.Param("id"), 10, 64)
	var input struct {
		Action string `json:"action"`
	}
	if lineID <= 0 || c.ShouldBindJSON(&input) != nil {
		return nil, hcError(errors.New("人工确认请求无效"))
	}
	line, statement, err := a.loadStatementLine(c, lineID)
	if err != nil {
		return nil, hcError(err)
	}
	uid, now := c.GetCurrentUid(), time.Now().Unix()
	switch input.Action {
	case "CONFIRM_NO_LEDGER":
		if line.LineKind != statementparser.LineKindTransfer && line.LineKind != statementparser.LineKindRepayment {
			return nil, hcError(errors.New("消费或收入流水必须分类或匹配已有交易，不能直接确认不入账"))
		}
		line.Status, line.MatchType, line.CoverageState, line.ReviewReason, line.RequiresReview = "EVIDENCE", "MANUAL_REVIEW", "VERIFIED", "人工确认不进入主账本", false
	case "CONFIRM_PLATFORM_LEDGER":
		if line.MatchType != "PLATFORM_UNRESOLVED" {
			return nil, hcError(errors.New("该流水不需要平台重复风险确认"))
		}
		if line.CategoryId <= 0 || (line.LineKind != statementparser.LineKindPurchase && line.LineKind != statementparser.LineKindRefund && line.LineKind != lineKindIncome) {
			return nil, hcError(errors.New("请先为消费、退款或收入流水选择有效分类"))
		}
		line.Status, line.MatchType, line.CoverageState, line.ReviewReason, line.RequiresReview = "CLASSIFIED", "MANUAL_PLATFORM_LEDGER", "VERIFIED", "人工确认与平台账单不重复，作为独立主账流水", false
	case "CONFIRM_PLATFORM_EVIDENCE":
		if line.MatchType != "PLATFORM_UNRESOLVED" {
			return nil, hcError(errors.New("该流水不需要平台重复风险确认"))
		}
		line.Status, line.MatchType, line.CoverageState, line.ReviewReason, line.RequiresReview = "EVIDENCE", "MANUAL_PLATFORM_EVIDENCE", "VERIFIED", "人工确认由支付宝/微信明细覆盖，不进入主账本", false
	default:
		return nil, hcError(errors.New("人工确认请求无效"))
	}
	if _, err = datastore.Container.UserDataStore.Query(c, uid).ID(line.Id).Where("uid = ?", uid).Cols("status", "match_type", "coverage_state", "review_reason", "requires_review").Update(line); err != nil {
		return nil, errs.ErrOperationFailed
	}
	if input.Action == "CONFIRM_PLATFORM_LEDGER" || input.Action == "CONFIRM_PLATFORM_EVIDENCE" {
		_, _ = datastore.Container.UserDataStore.Query(c, uid).Insert(&hengcai.ReconciliationMatch{Uid: uid, Month: statementMonth(statement), StatementLineId: line.Id, MatchType: line.MatchType, ScoreBps: 10000, Status: "CONFIRMED", Reason: line.ReviewReason, CreatedUnixTime: now})
	}
	return line, nil
}

func (a *HengcaiApi) verifyManualMarkerDimension(c *core.WebContext, transactionID int64, dimension string) error {
	if transactionID <= 0 {
		return nil
	}
	update := &hengcai.ManualTransactionMarker{UpdatedUnixTime: time.Now().Unix()}
	cols := []string{"updated_unix_time"}
	if dimension == "FUNDING_SOURCE" {
		update.FundingState = "VERIFIED"
		cols = append(cols, "funding_state")
	} else {
		update.MerchantState = "VERIFIED"
		cols = append(cols, "merchant_state")
	}
	_, err := datastore.Container.UserDataStore.Query(c, c.GetCurrentUid()).Where("uid = ? AND transaction_id = ?", c.GetCurrentUid(), transactionID).Cols(cols...).Update(update)
	return err
}

func (a *HengcaiApi) saveManualMarker(c *core.WebContext) (any, *errs.Error) {
	var input struct {
		TransactionId   int64  `json:"transaction_id,string"`
		MerchantChannel string `json:"merchant_channel"`
		FundingSource   string `json:"funding_source"`
	}
	if c.ShouldBindJSON(&input) != nil || input.TransactionId <= 0 {
		return nil, hcError(errors.New("交易来源标记无效"))
	}
	allowedChannels := map[string]bool{"": true, "ALIPAY": true, "WECHAT": true, "BANK_DIRECT": true, "OTHER": true}
	allowedFunding := map[string]bool{"": true, "BANK_ACCOUNT": true, "CREDIT_CARD": true, "CASH": true, "OTHER": true}
	input.MerchantChannel, input.FundingSource = strings.ToUpper(cleanCell(input.MerchantChannel)), strings.ToUpper(cleanCell(input.FundingSource))
	if !allowedChannels[input.MerchantChannel] || !allowedFunding[input.FundingSource] {
		return nil, hcError(errors.New("消费渠道或资金来源不受支持"))
	}
	uid := c.GetCurrentUid()
	var transaction models.Transaction
	if has, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND transaction_id = ? AND deleted = ?", uid, input.TransactionId, false).Get(&transaction); err != nil || !has {
		return nil, hcError(errors.New("核心交易不存在"))
	}
	var marker hengcai.ManualTransactionMarker
	has, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND transaction_id = ?", uid, input.TransactionId).Get(&marker)
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	now := time.Now().Unix()
	if has {
		merchantState, fundingState := marker.MerchantState, marker.FundingState
		if marker.MerchantChannel != input.MerchantChannel {
			merchantState = "PROVISIONAL"
		}
		if marker.FundingSource != input.FundingSource {
			fundingState = "PROVISIONAL"
		}
		_, err = datastore.Container.UserDataStore.Query(c, uid).ID(marker.Id).Cols("merchant_channel", "funding_source", "merchant_state", "funding_state", "updated_unix_time").Update(&hengcai.ManualTransactionMarker{MerchantChannel: input.MerchantChannel, FundingSource: input.FundingSource, MerchantState: merchantState, FundingState: fundingState, UpdatedUnixTime: now})
	} else {
		marker = hengcai.ManualTransactionMarker{Uid: uid, TransactionId: input.TransactionId, MerchantChannel: input.MerchantChannel, FundingSource: input.FundingSource, MerchantState: "PROVISIONAL", FundingState: "PROVISIONAL", UpdatedUnixTime: now}
		_, err = datastore.Container.UserDataStore.Query(c, uid).Insert(&marker)
	}
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	return map[string]any{"transaction_id": strconv.FormatInt(input.TransactionId, 10), "merchant_channel": input.MerchantChannel, "funding_source": input.FundingSource}, nil
}

func (a *HengcaiApi) getManualMarker(c *core.WebContext) (any, *errs.Error) {
	transactionID, _ := strconv.ParseInt(c.Query("transaction_id"), 10, 64)
	if transactionID <= 0 {
		return nil, hcError(errors.New("transaction_id 无效"))
	}
	var marker hengcai.ManualTransactionMarker
	has, err := datastore.Container.UserDataStore.Query(c, c.GetCurrentUid()).Where("uid = ? AND transaction_id = ?", c.GetCurrentUid(), transactionID).Get(&marker)
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	if !has {
		return map[string]any{"transaction_id": strconv.FormatInt(transactionID, 10), "merchant_channel": "", "funding_source": "", "merchant_state": "PROVISIONAL", "funding_state": "PROVISIONAL"}, nil
	}
	return marker, nil
}

func dateDistanceDays(a, b string) int {
	ta, ea := time.Parse("2006-01-02", a)
	tb, eb := time.Parse("2006-01-02", b)
	if ea != nil || eb != nil {
		return 999
	}
	d := int(ta.Sub(tb).Hours() / 24)
	if d < 0 {
		return -d
	}
	return d
}

func (a *HengcaiApi) reconciliationDashboard(c *core.WebContext) (any, *errs.Error) {
	month := c.Query("month")
	if !validMonth(month) {
		month = time.Now().Format("2006-01")
	}
	uid := c.GetCurrentUid()
	parsedMonth, _ := time.Parse("2006-01", month)
	monthStart := parsedMonth.Format("2006-01-02")
	monthEnd := time.Date(parsedMonth.Year(), parsedMonth.Month()+1, 0, 0, 0, 0, 0, time.Local).Format("2006-01-02")
	var statements []*hengcai.StatementImport
	_ = datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND period_start <= ? AND period_end >= ?", uid, monthEnd, monthStart).Desc("id").Find(&statements)
	// Reconcile already imported bank statements as well. This is needed
	// when the rule set changes after a statement was uploaded; the dashboard
	// remains the natural place to refresh derived matching state.
	for _, statement := range statements {
		if statement.Provider != statementparser.DebitStatementProvider && statement.Provider != "CMB" {
			continue
		}
		statementLines := make([]*hengcai.StatementLine, 0)
		if err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND statement_id = ?", uid, statement.Id).Find(&statementLines); err == nil && len(statementLines) > 0 {
			_ = a.buildReconciliationMatches(c, statement, statementLines)
		}
	}
	var lines []*hengcai.StatementLine
	_ = datastore.Container.UserDataStore.Query(c, uid).Table("hengcai_statement_line").Join("INNER", "hengcai_statement_import", "hengcai_statement_line.statement_id = hengcai_statement_import.id").Where("hengcai_statement_line.uid = ? AND hengcai_statement_import.period_start <= ? AND hengcai_statement_import.period_end >= ?", uid, monthEnd, monthStart).Asc("hengcai_statement_line.posted_date").Find(&lines)
	transactions, _ := services.Transactions.GetTransactionsInMonthByPage(c, uid, int32(parsedMonth.Year()), int32(parsedMonth.Month()), 0, nil, nil, nil, false, "", "", core.MATCH_MODE_DEFAULT, false)
	transactionIDs := make(map[int64]bool, len(transactions))
	for _, transaction := range transactions {
		transactionIDs[transaction.TransactionId] = true
	}
	allMarkers := make([]*hengcai.ManualTransactionMarker, 0)
	_ = datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Find(&allMarkers)
	monthMarkers := make([]*hengcai.ManualTransactionMarker, 0)
	for _, marker := range allMarkers {
		if transactionIDs[marker.TransactionId] {
			monthMarkers = append(monthMarkers, marker)
		}
	}
	coverages := make([]*hengcai.TransactionCoverage, 0)
	_ = datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Desc("updated_unix_time").Find(&coverages)
	var expense, income int64
	for _, tx := range transactions {
		if tx.Type == models.TRANSACTION_DB_TYPE_EXPENSE {
			expense += tx.Amount
		}
		if tx.Type == models.TRANSACTION_DB_TYPE_INCOME {
			income += tx.Amount
		}
	}
	var close hengcai.MonthClose
	closed, _ := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ? AND month = ?", uid, month).Get(&close)
	counts := map[string]int{}
	for _, line := range lines {
		counts[line.Status]++
	}
	accounts, _ := services.Accounts.GetAllAccountsByUid(c, uid)
	categories, _ := services.TransactionCategories.GetAllCategoriesByUid(c, uid, models.CATEGORY_TYPE_EXPENSE, -1)
	incomeCategories, _ := services.TransactionCategories.GetAllCategoriesByUid(c, uid, models.CATEGORY_TYPE_INCOME, -1)
	categories = append(categories, incomeCategories...)
	categories = postableCategories(categories)
	categoryList := make([]map[string]any, 0, len(categories))
	for _, category := range categories {
		categoryList = append(categoryList, map[string]any{"category_id": strconv.FormatInt(category.CategoryId, 10), "name": category.Name, "type": category.Type})
	}
	var capexPurchases []*hengcai.CapexPurchase
	var capexInstallments []*hengcai.CapexInstallment
	var capexSettlements []*hengcai.CapexInstallmentSettlement
	_ = datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Asc("purchase_date").Find(&capexPurchases)
	_ = datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Asc("due_date").Find(&capexInstallments)
	_ = datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Find(&capexSettlements)
	return map[string]any{"month": month, "core_expense_minor": expense, "core_income_minor": income, "core_transaction_count": len(transactions), "statements": statements, "lines": lines, "status_counts": counts, "manual_markers": monthMarkers, "coverages": coverages, "month_close": func() any {
		if closed {
			return close
		}
		return nil
	}(), "accounts": accounts, "categories": categoryList, "capex_purchases": capexPurchases, "capex_installments": capexInstallments, "capex_settlements": capexSettlements}, nil
}

func (a *HengcaiApi) reopenMonth(c *core.WebContext) (any, *errs.Error) {
	var input struct {
		Month string `json:"month"`
	}
	if c.ShouldBindJSON(&input) != nil || !validMonth(input.Month) {
		return nil, hcError(errors.New("month 必须为 YYYY-MM"))
	}
	_, err := datastore.Container.UserDataStore.Query(c, c.GetCurrentUid()).Where("uid = ? AND month = ?", c.GetCurrentUid(), input.Month).Cols("status", "closed_unix_time").Update(&hengcai.MonthClose{Status: "REOPENED", ClosedUnixTime: 0})
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	return map[string]any{"month": input.Month, "status": "REOPENED"}, nil
}

func validAIBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) && u.Host != ""
}

func (a *HengcaiApi) saveAISetting(c *core.WebContext) (any, *errs.Error) {
	var input hengcai.AISetting
	if c.ShouldBindJSON(&input) != nil {
		return nil, hcError(errors.New("AI 设置格式无效"))
	}
	input.Provider, input.BaseUrl, input.Model = cleanCell(input.Provider), strings.TrimRight(cleanCell(input.BaseUrl), "/"), cleanCell(input.Model)
	uid := c.GetCurrentUid()
	var existing hengcai.AISetting
	has, _ := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Get(&existing)
	if input.ApiKey == "" && has {
		input.ApiKey = existing.ApiKey
	} else if input.ApiKey != "" {
		sealed, err := sealAlpacaSecret(a.CurrentConfig().SecretKey, input.ApiKey)
		if err != nil {
			return nil, errs.ErrOperationFailed
		}
		input.ApiKey = sealed
	}
	if input.Enabled && (input.Provider == "" || !validAIBaseURL(input.BaseUrl) || input.Model == "" || input.ApiKey == "") {
		return nil, hcError(errors.New("启用 AI 分类时，提供商、API 地址、模型和 API Key 均不能为空"))
	}
	input.Uid, input.UpdatedUnixTime = uid, time.Now().Unix()
	if has {
		input.Id = existing.Id
		_, _ = datastore.Container.UserDataStore.Query(c, uid).ID(existing.Id).Cols("enabled", "provider", "base_url", "api_key", "model", "updated_unix_time").Update(&input)
	} else if _, err := datastore.Container.UserDataStore.Query(c, uid).Insert(&input); err != nil {
		return nil, errs.ErrOperationFailed
	}
	return map[string]any{"configured": input.ApiKey != "", "enabled": input.Enabled}, nil
}

func (a *HengcaiApi) getAISetting(c *core.WebContext) (any, *errs.Error) {
	var setting hengcai.AISetting
	has, err := datastore.Container.UserDataStore.Query(c, c.GetCurrentUid()).Where("uid = ?", c.GetCurrentUid()).Get(&setting)
	if err != nil {
		return nil, errs.ErrOperationFailed
	}
	if !has {
		return map[string]any{"provider": "openai_compatible", "base_url": "https://api.openai.com/v1", "model": "gpt-4.1-mini", "enabled": false, "configured": false}, nil
	}
	return map[string]any{"provider": setting.Provider, "base_url": setting.BaseUrl, "model": setting.Model, "enabled": setting.Enabled, "configured": setting.ApiKey != "", "updated_unix_time": setting.UpdatedUnixTime}, nil
}

func (a *HengcaiApi) testAISetting(c *core.WebContext) (any, *errs.Error) {
	var setting hengcai.AISetting
	has, _ := datastore.Container.UserDataStore.Query(c, c.GetCurrentUid()).Where("uid = ?", c.GetCurrentUid()).Get(&setting)
	if !has || setting.ApiKey == "" {
		return nil, hcError(errors.New("请先保存 AI 设置"))
	}
	key, err := openAlpacaSecret(a.CurrentConfig().SecretKey, setting.ApiKey)
	if err != nil {
		return nil, hcError(errors.New("AI API Key 无法解密，请重新保存"))
	}
	req, _ := http.NewRequestWithContext(c, http.MethodGet, strings.TrimRight(setting.BaseUrl, "/")+"/models", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, hcError(fmt.Errorf("AI 连接失败: %v", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, hcError(fmt.Errorf("AI 提供商返回 HTTP %d: %s", resp.StatusCode, cleanCell(string(body))))
	}
	return map[string]any{"message": "AI API 只读连接成功", "model": setting.Model}, nil
}

func (a *HengcaiApi) classifyLinesWithUserAI(c *core.WebContext, lines []*hengcai.StatementLine, categories []*models.TransactionCategory) (bool, error) {
	uid := c.GetCurrentUid()
	var setting hengcai.AISetting
	has, err := datastore.Container.UserDataStore.Query(c, uid).Where("uid = ?", uid).Get(&setting)
	if err != nil || !has || !setting.Enabled {
		return false, err
	}
	log.Infof(c, "[hengcai_reconciliation.classifyLinesWithUserAI] user \"uid:%d\" starts AI classification, statement lines=%d, categories=%d, provider=%s, model=%s", uid, len(lines), len(categories), setting.Provider, setting.Model)
	key, err := openAlpacaSecret(a.CurrentConfig().SecretKey, setting.ApiKey)
	if err != nil {
		return true, errors.New("AI API Key 无法解密，请在基础设置中重新保存")
	}
	allowed := make(map[int64]bool, len(categories))
	categoryByID := make(map[int64]models.TransactionCategoryType, len(categories))
	expenseCategoryText := make([]string, 0, len(categories))
	incomeCategoryText := make([]string, 0, len(categories))
	for _, category := range categories {
		allowed[category.CategoryId] = true
		categoryByID[category.CategoryId] = category.Type
		if category.Type == models.CATEGORY_TYPE_INCOME {
			incomeCategoryText = append(incomeCategoryText, fmt.Sprintf("%d=%s", category.CategoryId, category.Name))
		} else {
			expenseCategoryText = append(expenseCategoryText, fmt.Sprintf("%d=%s", category.CategoryId, category.Name))
		}
	}
	categoryText := []string{"分类（支出）:" + strings.Join(expenseCategoryText, ","), "分类（收入）:" + strings.Join(incomeCategoryText, ",")}
	// Group lines by (支出/收入, 账单分类, 商户摘要) so the same consumption
	// always gets the same category, and each group needs only one AI request.
	type aiClassifyGroup struct {
		kind           models.TransactionCategoryType
		representative *hengcai.StatementLine
		lineHashes     []string
	}
	groups := make([]*aiClassifyGroup, 0)
	groupIndex := make(map[string]*aiClassifyGroup)
	for _, line := range lines {
		var kind models.TransactionCategoryType
		switch line.LineKind {
		case statementparser.LineKindPurchase, statementparser.LineKindRefund:
			kind = models.CATEGORY_TYPE_EXPENSE
		case lineKindIncome:
			kind = models.CATEGORY_TYPE_INCOME
		default:
			continue
		}
		key := normalizeCategoryName(line.StatementCategory) + "|" + normalizeCategoryName(line.Description)
		if key == "|" {
			key = line.LineHash
		}
		key = fmt.Sprintf("%d|%s", kind, key)
		group := groupIndex[key]
		if group == nil {
			group = &aiClassifyGroup{kind: kind, representative: line}
			groupIndex[key] = group
			groups = append(groups, group)
		}
		group.lineHashes = append(group.lineHashes, line.LineHash)
	}
	lineText := make([]string, 0, len(groups))
	groupByRepHash := make(map[string]*aiClassifyGroup, len(groups))
	for _, group := range groups {
		rep := group.representative
		text := fmt.Sprintf("%s|%s|%d", rep.LineHash, rep.Description, rep.AmountMinor)
		if statementCategory := cleanCell(rep.StatementCategory); statementCategory != "" {
			text += "|原始分类=" + statementCategory
		}
		text += fmt.Sprintf("|同组%d条", len(group.lineHashes))
		prefix := "支出:"
		if group.kind == models.CATEGORY_TYPE_INCOME {
			prefix = "收入:"
		}
		lineText = append(lineText, prefix+text)
		groupByRepHash[rep.LineHash] = group
	}
	if len(lineText) == 0 {
		return true, nil
	}
	// 分批调用 AI，避免整张账单一次性生成长 JSON 导致上游响应超时
	batchCount := (len(lineText) + aiClassifyBatchSize - 1) / aiClassifyBatchSize
	results := make([]*aiClassifyBatchOutcome, batchCount)
	semaphore := make(chan struct{}, aiClassifyMaxConcurrentBatches)
	var wg sync.WaitGroup
	for batchIndex := 0; batchIndex < batchCount; batchIndex++ {
		wg.Add(1)
		go func(batchIndex int) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			start := batchIndex * aiClassifyBatchSize
			end := start + aiClassifyBatchSize
			if end > len(lineText) {
				end = len(lineText)
			}
			batch := lineText[start:end]

			var suggestions []aiClassifySuggestion
			var batchErr error
			for attempt := 1; attempt <= aiClassifyBatchMaxAttempts; attempt++ {
				attemptStartTime := time.Now()
				suggestions, batchErr = a.classifyBatchWithUserAI(c, setting.BaseUrl, key, setting.Model, categoryText, batch)
				if batchErr == nil {
					log.Infof(c, "[hengcai_reconciliation.classifyLinesWithUserAI] user \"uid:%d\" batch %d/%d request ok in %s, returned=%d", uid, batchIndex+1, batchCount, time.Since(attemptStartTime).Round(time.Millisecond), len(suggestions))
					break
				}
				log.Warnf(c, "[hengcai_reconciliation.classifyLinesWithUserAI] user \"uid:%d\" batch %d/%d attempt %d/%d failed in %s: %s", uid, batchIndex+1, batchCount, attempt, aiClassifyBatchMaxAttempts, time.Since(attemptStartTime).Round(time.Millisecond), batchErr.Error())
				if attempt < aiClassifyBatchMaxAttempts {
					select {
					case <-time.After(time.Duration(attempt) * time.Second):
					case <-c.Done():
						results[batchIndex] = &aiClassifyBatchOutcome{err: fmt.Errorf("AI 分类请求已取消（第 %d/%d 批）", batchIndex+1, batchCount)}
						return
					}
				}
			}
			results[batchIndex] = &aiClassifyBatchOutcome{suggestions: suggestions, err: batchErr}
		}(batchIndex)
	}
	wg.Wait()

	sess := datastore.Container.UserDataStore.Query(c, uid)
	var firstErr error
	firstErrBatch := 0
	for batchIndex, result := range results {
		if result.err != nil {
			log.Errorf(c, "[hengcai_reconciliation.classifyLinesWithUserAI] user \"uid:%d\" batch %d/%d failed after %d attempts: %s", uid, batchIndex+1, batchCount, aiClassifyBatchMaxAttempts, result.err.Error())
			if firstErr == nil {
				firstErr = result.err
				firstErrBatch = batchIndex + 1
			}
			continue
		}
		applied := 0
		for _, suggestion := range result.suggestions {
			if !allowed[suggestion.CategoryID] {
				continue
			}
			group, ok := groupByRepHash[suggestion.LineHash]
			if !ok || categoryByID[suggestion.CategoryID] != group.kind {
				continue
			}
			confidence := suggestion.Confidence
			if confidence < 0 {
				confidence = 0
			}
			if confidence > 10000 {
				confidence = 10000
			}
			for _, lineHash := range group.lineHashes {
				affected, _ := sess.Where("uid = ? AND line_hash = ? AND counterparty_type <> ?", uid, lineHash, "PERSON").Cols("category_id", "classification", "confidence_bps", "status").Update(&hengcai.StatementLine{CategoryId: suggestion.CategoryID, Classification: "AI 建议", ConfidenceBps: confidence, Status: "CLASSIFIED"})
				applied += int(affected)
			}
		}
		log.Infof(c, "[hengcai_reconciliation.classifyLinesWithUserAI] user \"uid:%d\" batch %d/%d applied, returned=%d, applied=%d", uid, batchIndex+1, batchCount, len(result.suggestions), applied)
	}
	if firstErr != nil {
		return true, fmt.Errorf("AI 分类连接失败（第 %d/%d 批失败，其余批次已完成）: %w", firstErrBatch, batchCount, firstErr)
	}
	return true, nil
}

func (a *HengcaiApi) classifyBatchWithUserAI(c *core.WebContext, baseURL, apiKey, model string, categoryText, lineText []string) ([]aiClassifySuggestion, error) {
	prompt := "将交易分类到下列已有分类。不得创造 category_id。支出流水只能选择支出分类，收入流水只能选择收入分类。若流水附有“原始分类=xxx”，请优先参考该名称并映射到最接近的已有分类。返回 JSON 对象 {\"suggestions\":[{\"line_hash\":string,\"category_id\":number,\"confidence_bps\":number}]}。\n" + strings.Join(categoryText, "\n") + "\n交易:" + strings.Join(lineText, ";")
	payload := map[string]any{"model": model, "temperature": 0, "response_format": map[string]string{"type": "json_object"}, "messages": []map[string]string{{"role": "system", "content": "你是严谨的个人记账分类器，只返回 JSON。"}, {"role": "user", "content": prompt}}}
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(c, http.MethodPost, strings.TrimRight(baseURL, "/")+"/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: aiClassifyBatchTimeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("AI 提供商返回 HTTP %d: %s", resp.StatusCode, cleanCell(string(responseBody)))
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(responseBody, &envelope) != nil || len(envelope.Choices) == 0 {
		return nil, errors.New("AI 返回了无效响应")
	}
	var result struct {
		Suggestions []aiClassifySuggestion `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(envelope.Choices[0].Message.Content), &result); err != nil {
		return nil, errors.New("AI 分类返回的不是有效 JSON")
	}
	return result.Suggestions, nil
}

func (a *HengcaiApi) ReconciliationUploadHandler(c *core.WebContext) (any, *errs.Error) {
	return a.reconciliationUpload(c)
}
func (a *HengcaiApi) ReconciliationDashboardHandler(c *core.WebContext) (any, *errs.Error) {
	return a.reconciliationDashboard(c)
}
func (a *HengcaiApi) ManualMarkerSaveHandler(c *core.WebContext) (any, *errs.Error) {
	return a.saveManualMarker(c)
}
func (a *HengcaiApi) ManualMarkerGetHandler(c *core.WebContext) (any, *errs.Error) {
	return a.getManualMarker(c)
}
func (a *HengcaiApi) StatementLineCandidatesHandler(c *core.WebContext) (any, *errs.Error) {
	return a.findStatementLineCandidates(c)
}
func (a *HengcaiApi) StatementLineManualMatchHandler(c *core.WebContext) (any, *errs.Error) {
	return a.manuallyMatchStatementLine(c)
}
func (a *HengcaiApi) StatementLineManualReviewHandler(c *core.WebContext) (any, *errs.Error) {
	return a.manuallyReviewStatementLine(c)
}
func (a *HengcaiApi) StatementLineRefundHandler(c *core.WebContext) (any, *errs.Error) {
	return a.updateLineAsRefund(c)
}
func (a *HengcaiApi) ReopenMonthHandler(c *core.WebContext) (any, *errs.Error) {
	return a.reopenMonth(c)
}
func (a *HengcaiApi) AISettingSaveHandler(c *core.WebContext) (any, *errs.Error) {
	return a.saveAISetting(c)
}
func (a *HengcaiApi) AISettingGetHandler(c *core.WebContext) (any, *errs.Error) {
	return a.getAISetting(c)
}
func (a *HengcaiApi) AISettingTestHandler(c *core.WebContext) (any, *errs.Error) {
	return a.testAISetting(c)
}
