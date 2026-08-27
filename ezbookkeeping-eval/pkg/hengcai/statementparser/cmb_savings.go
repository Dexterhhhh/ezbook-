package statementparser

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	pdf "github.com/ledongthuc/pdf"
)

const (
	DebitStatementProvider = "CMB_SAVINGS"
	LineKindIncome         = "INCOME"
	LineKindTransfer       = "TRANSFER"
)

var (
	cmbSavingsMarker        = "招商银行交易流水"
	cmbSavingsPeriodPattern = regexp.MustCompile(`(\d{4}-\d{2}-\d{2})\s*[-—]+\s*(\d{4}-\d{2}-\d{2})`)
	cmbSavingsLinePattern   = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})\s+([A-Z]{3})\s+(-?[\d,]+\.\d{2})\s+(-?[\d,]+\.\d{2})\s+(.+)$`)
	cmbSavingsHolderPattern = regexp.MustCompile(`户\s*名\s*[：:]\s*([^\s]+)`)
	cmbPersonNamePattern    = regexp.MustCompile(`^[\p{Han}]{2,4}$`)
)

var cmbSavingsTransactionSummaries = []string{
	"银证转账(第三方存管)",
	"代发住房公积金",
	"银联无卡自助消费",
	"银联快捷支付",
	"掌上生活还款",
	"转账汇款",
	"快捷退款",
	"银联代付",
	"快捷支付",
	"网联收款",
}

type DebitCardStatement struct {
	Provider             string          `json:"provider"`
	StatementType        string          `json:"statement_type"`
	AccountHolder        string          `json:"account_holder,omitempty"`
	Currency             string          `json:"currency"`
	StatementDate        string          `json:"statement_date"`
	StatementPeriodStart string          `json:"statement_period_start"`
	StatementPeriodEnd   string          `json:"statement_period_end"`
	OpeningBalanceMinor  int64           `json:"opening_balance_minor"`
	ClosingBalanceMinor  int64           `json:"closing_balance_minor"`
	TotalDebitMinor      int64           `json:"total_debit_minor"`
	TotalCreditMinor     int64           `json:"total_credit_minor"`
	ArtifactSHA256       string          `json:"artifact_sha256,omitempty"`
	BalanceValid         bool            `json:"balance_valid"`
	SummaryValid         bool            `json:"summary_valid"`
	NeedsReview          bool            `json:"needs_review"`
	ManualReviewCount    int             `json:"manual_review_count"`
	ValidationErrors     []string        `json:"validation_errors"`
	UnrecognizedRows     []string        `json:"unrecognized_rows,omitempty"`
	Lines                []DebitCardLine `json:"lines"`
}

type DebitCardLine struct {
	LineNumber           int    `json:"line_number"`
	PostedDate           string `json:"posted_date"`
	Description          string `json:"description"`
	Counterparty         string `json:"counterparty"`
	CounterpartyType     string `json:"counterparty_type"`
	Currency             string `json:"currency"`
	AmountMinor          int64  `json:"amount_minor"`
	SignedAmountMinor    int64  `json:"signed_amount_minor"`
	BalanceMinor         int64  `json:"balance_minor"`
	Direction            string `json:"direction"`
	TransactionType      string `json:"transaction_type"`
	LineKind             string `json:"line_kind"`
	AccountingTreatment  string `json:"accounting_treatment"`
	PaymentChannel       string `json:"payment_channel"`
	MerchantChannel      string `json:"merchant_channel"`
	FundingSource        string `json:"funding_source"`
	RequiresManualReview bool   `json:"requires_manual_review"`
	ReviewReason         string `json:"review_reason,omitempty"`
	LineHash             string `json:"line_hash"`
}

// ParseCMBSavingsPDF parses the text-based CMB savings-account transaction
// statement. Personal counterparties are deliberately marked for manual
// review unless the bank's summary is an explicit automated settlement
// label such as 快捷支付 or 快捷退款.
func ParseCMBSavingsPDF(readerAt io.ReaderAt, size int64) (DebitCardStatement, error) {
	reader, err := pdf.NewReader(readerAt, size)
	if err != nil {
		return DebitCardStatement{}, fmt.Errorf("open PDF: %w", err)
	}

	pages := make([][]string, 0, reader.NumPage())
	for pageNumber := 1; pageNumber <= reader.NumPage(); pageNumber++ {
		rows, rowErr := reader.Page(pageNumber).GetTextByRow()
		if rowErr != nil {
			return DebitCardStatement{}, fmt.Errorf("extract page %d: %w", pageNumber, rowErr)
		}
		pageRows := make([]string, 0, len(rows))
		for _, row := range rows {
			text := joinPDFRow(row.Content)
			if text != "" {
				pageRows = append(pageRows, text)
			}
		}
		pages = append(pages, pageRows)
	}

	statement, err := ParseCMBSavingsRows(pages)
	if err != nil {
		return DebitCardStatement{}, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, io.NewSectionReader(readerAt, 0, size)); err != nil {
		return DebitCardStatement{}, fmt.Errorf("hash PDF: %w", err)
	}
	statement.ArtifactSHA256 = hex.EncodeToString(hash.Sum(nil))
	return statement, nil
}

func ParseCMBSavingsText(text string) (DebitCardStatement, error) {
	return ParseCMBSavingsRows([][]string{strings.Split(text, "\n")})
}

func ParseCMBSavingsRows(pages [][]string) (DebitCardStatement, error) {
	rows := make([]string, 0)
	for _, page := range pages {
		for _, row := range page {
			row = normalizeSpaces(row)
			if row != "" {
				rows = append(rows, row)
			}
		}
	}
	allText := strings.Join(rows, "\n")
	if !strings.Contains(allText, cmbSavingsMarker) {
		return DebitCardStatement{}, errors.New("document is not a supported CMB savings-account statement")
	}
	periodMatch := cmbSavingsPeriodPattern.FindStringSubmatch(allText)
	if len(periodMatch) != 3 {
		return DebitCardStatement{}, errors.New("CMB savings statement period not found")
	}

	statement := DebitCardStatement{
		Provider:             DebitStatementProvider,
		StatementType:        "SAVINGS_ACCOUNT",
		AccountHolder:        findCMBAccountHolder(rows),
		Currency:             "CNY",
		StatementDate:        periodMatch[2],
		StatementPeriodStart: periodMatch[1],
		StatementPeriodEnd:   periodMatch[2],
		ValidationErrors:     make([]string, 0),
		UnrecognizedRows:     make([]string, 0),
		Lines:                make([]DebitCardLine, 0),
	}
	for _, row := range rows {
		matches := cmbSavingsLinePattern.FindStringSubmatch(row)
		if len(matches) == 0 {
			continue
		}
		line, parseErr := parseCMBSavingsLine(matches, len(statement.Lines)+1)
		if parseErr != nil {
			statement.UnrecognizedRows = append(statement.UnrecognizedRows, row)
			statement.ValidationErrors = append(statement.ValidationErrors, parseErr.Error())
			continue
		}
		statement.Lines = append(statement.Lines, line)
		if statement.OpeningBalanceMinor == 0 && len(statement.Lines) == 1 {
			statement.OpeningBalanceMinor = line.BalanceMinor - line.SignedAmountMinor
		}
		if line.SignedAmountMinor < 0 {
			statement.TotalDebitMinor += line.AmountMinor
		} else {
			statement.TotalCreditMinor += line.AmountMinor
		}
		if line.RequiresManualReview {
			statement.ManualReviewCount++
		}
	}
	if len(statement.Lines) == 0 {
		return DebitCardStatement{}, errors.New("no transaction rows were recognized")
	}
	statement.ClosingBalanceMinor = statement.Lines[len(statement.Lines)-1].BalanceMinor
	statement.BalanceValid = validateCMBRunningBalances(statement)
	statement.SummaryValid = statement.BalanceValid && len(statement.UnrecognizedRows) == 0
	if !statement.BalanceValid {
		statement.ValidationErrors = append(statement.ValidationErrors, "running balance does not reconcile with transaction amounts")
	}
	if len(statement.UnrecognizedRows) > 0 {
		statement.ValidationErrors = append(statement.ValidationErrors, fmt.Sprintf("%d transaction-like rows were not recognized", len(statement.UnrecognizedRows)))
	}
	statement.NeedsReview = statement.ManualReviewCount > 0 || len(statement.ValidationErrors) > 0
	return statement, nil
}

func findCMBAccountHolder(rows []string) string {
	for _, row := range rows {
		if match := cmbSavingsHolderPattern.FindStringSubmatch(row); len(match) == 2 {
			return strings.TrimSpace(match[1])
		}
	}
	return ""
}

func parseCMBSavingsLine(matches []string, lineNumber int) (DebitCardLine, error) {
	signedAmount, err := parseMoney(matches[3])
	if err != nil {
		return DebitCardLine{}, fmt.Errorf("line %d amount: %w", lineNumber, err)
	}
	balance, err := parseMoney(matches[4])
	if err != nil {
		return DebitCardLine{}, fmt.Errorf("line %d balance: %w", lineNumber, err)
	}
	summary, counterparty := splitCMBSavingsSummary(matches[5])
	if summary == "" || counterparty == "" {
		return DebitCardLine{}, fmt.Errorf("line %d transaction summary or counterparty is missing", lineNumber)
	}
	lineKind, treatment := classifyCMBSavingsLine(summary, signedAmount)
	if isCMBAutoHandledSummary(summary) {
		treatment = "PLATFORM_SETTLEMENT_EXCLUDED"
	}
	counterpartyType := classifyCMBCounterparty(counterparty)
	requiresManual := counterpartyType == "PERSON" && !isCMBAutoHandledSummary(summary)
	reviewReason := ""
	if requiresManual {
		reviewReason = "个人对手方必须人工确认"
	}
	direction := "DEBIT"
	if signedAmount >= 0 {
		direction = "CREDIT"
	}
	merchantChannel := savingsMerchantChannel(summary + " " + counterparty)
	lineHashInput := fmt.Sprintf("%d|%s|%s|%s|%d|%d|%s|%s", lineNumber, matches[1], summary, counterparty, signedAmount, balance, lineKind, counterpartyType)
	hash := sha256.Sum256([]byte(lineHashInput))
	return DebitCardLine{
		LineNumber:           lineNumber,
		PostedDate:           matches[1],
		Description:          summary,
		Counterparty:         counterparty,
		CounterpartyType:     counterpartyType,
		Currency:             matches[2],
		AmountMinor:          absAmount(signedAmount),
		SignedAmountMinor:    signedAmount,
		BalanceMinor:         balance,
		Direction:            direction,
		TransactionType:      summary,
		LineKind:             lineKind,
		AccountingTreatment:  treatment,
		PaymentChannel:       summary,
		MerchantChannel:      merchantChannel,
		FundingSource:        "BANK_ACCOUNT",
		RequiresManualReview: requiresManual,
		ReviewReason:         reviewReason,
		LineHash:             hex.EncodeToString(hash[:]),
	}, nil
}

func splitCMBSavingsSummary(value string) (string, string) {
	value = normalizeSpaces(value)
	for _, summary := range cmbSavingsTransactionSummaries {
		if strings.HasPrefix(value, summary+" ") {
			return summary, strings.TrimSpace(strings.TrimPrefix(value, summary))
		}
	}
	return "", ""
}

func classifyCMBSavingsLine(summary string, signedAmount int64) (string, string) {
	switch {
	case strings.Contains(summary, "还款"):
		return LineKindRepayment, "TRANSFER_TO_CREDIT_CARD"
	case strings.Contains(summary, "退款"):
		return LineKindRefund, "REFUND_MATCH_CANDIDATE"
	case strings.Contains(summary, "转账"):
		return LineKindTransfer, "ACCOUNT_TRANSFER_REVIEW"
	case signedAmount < 0:
		return LineKindPurchase, "EXPENSE_CANDIDATE"
	default:
		return LineKindIncome, "INCOME_CANDIDATE"
	}
}

func isCMBAutoHandledSummary(summary string) bool {
	return summary == "快捷支付" || summary == "快捷退款"
}

func classifyCMBCounterparty(counterparty string) string {
	counterparty = strings.TrimSpace(counterparty)
	for _, marker := range []string{"有限公司", "银行", "支付", "平台", "商城", "旗舰店", "商户", "中心", "公积金", "证券", "基金", "店", "微信", "支付宝", "财付通", "转账"} {
		if strings.Contains(counterparty, marker) {
			return "ORGANIZATION"
		}
	}
	if cmbPersonNamePattern.MatchString(counterparty) {
		return "PERSON"
	}
	return "UNKNOWN"
}

func savingsMerchantChannel(value string) string {
	v := strings.ToUpper(value)
	if strings.Contains(v, "支付宝") || strings.Contains(v, "ALIPAY") {
		return "ALIPAY"
	}
	if strings.Contains(v, "微信") || strings.Contains(v, "财付通") || strings.Contains(v, "WECHAT") || strings.Contains(v, "TENPAY") {
		return "WECHAT"
	}
	return "BANK_DIRECT"
}

func validateCMBRunningBalances(statement DebitCardStatement) bool {
	balance := statement.OpeningBalanceMinor
	for _, line := range statement.Lines {
		balance += line.SignedAmountMinor
		if balance != line.BalanceMinor {
			return false
		}
	}
	return balance == statement.ClosingBalanceMinor
}

func absAmount(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
