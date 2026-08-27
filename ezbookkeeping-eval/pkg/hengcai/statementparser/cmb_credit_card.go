package statementparser

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	pdf "github.com/ledongthuc/pdf"
)

const (
	LineKindPurchase             = "PURCHASE"
	LineKindRepayment            = "REPAYMENT"
	LineKindRefund               = "REFUND"
	LineKindCashback             = "CASHBACK"
	LineKindInstallmentPrincipal = "INSTALLMENT_PRINCIPAL"
	LineKindInstallmentInterest  = "INSTALLMENT_INTEREST"
	LineKindInstallmentSetup     = "INSTALLMENT_SETUP"
	LineKindInterestSubsidy      = "INTEREST_SUBSIDY"
	LineKindAdjustment           = "ADJUSTMENT"
)

var (
	cmbStatementMarker      = "招商银行信用卡对账单"
	dateCNPattern           = regexp.MustCompile(`\d{4}年\d{2}月\d{2}日`)
	moneyPattern            = regexp.MustCompile(`(?:¥|￥)\s*(-?[\d,]+\.\d{2})`)
	transactionStartPattern = regexp.MustCompile(`^\d{2}/\d{2}\b`)
	transactionPattern      = regexp.MustCompile(
		`^(\d{2}/\d{2})(?:\s+(\d{2}/\d{2}))?\s+(.+?)\s+(-?[\d,]+\.\d{2})\s+([0-9A-Za-z]{4})\s+(-?[\d,]+\.\d{2})(?:\(([A-Z]{2})\))?$`,
	)
)

type CreditCardStatement struct {
	Provider                string           `json:"provider"`
	StatementType           string           `json:"statement_type"`
	Currency                string           `json:"currency"`
	StatementDate           string           `json:"statement_date"`
	PaymentDueDate          string           `json:"payment_due_date"`
	StatementPeriodStart    string           `json:"statement_period_start"`
	StatementPeriodEnd      string           `json:"statement_period_end"`
	PeriodDerivation        string           `json:"period_derivation"`
	ObservedPostingStart    string           `json:"observed_posting_start,omitempty"`
	CreditLimitMinor        int64            `json:"credit_limit_minor"`
	OpeningBalanceMinor     int64            `json:"opening_balance_minor"`
	ClosingBalanceMinor     int64            `json:"closing_balance_minor"`
	MinimumPaymentMinor     int64            `json:"minimum_payment_minor"`
	PreviousPaymentsMinor   int64            `json:"previous_payments_minor"`
	NewChargesMinor         int64            `json:"new_charges_minor"`
	AdjustmentsMinor        int64            `json:"adjustments_minor"`
	InterestMinor           int64            `json:"interest_minor"`
	TotalDebitMinor         int64            `json:"total_debit_minor"`
	TotalCreditMinor        int64            `json:"total_credit_minor"`
	RepaymentCreditMinor    int64            `json:"repayment_credit_minor"`
	CashbackCreditMinor     int64            `json:"cashback_credit_minor"`
	NonRepaymentCreditMinor int64            `json:"non_repayment_credit_minor"`
	ArtifactSHA256          string           `json:"artifact_sha256,omitempty"`
	BalanceValid            bool             `json:"balance_valid"`
	SummaryValid            bool             `json:"summary_valid"`
	NeedsReview             bool             `json:"needs_review"`
	ValidationErrors        []string         `json:"validation_errors"`
	UnrecognizedRows        []string         `json:"unrecognized_rows,omitempty"`
	Lines                   []CreditCardLine `json:"lines"`
}

type CreditCardLine struct {
	LineNumber            int     `json:"line_number"`
	TransactionDate       *string `json:"transaction_date,omitempty"`
	PostedDate            string  `json:"posted_date"`
	Description           string  `json:"description"`
	AmountMinor           int64   `json:"amount_minor"`
	SignedAmountMinor     int64   `json:"signed_amount_minor"`
	Direction             string  `json:"direction"`
	CardLastFour          string  `json:"card_last_four"`
	OriginalAmountMinor   int64   `json:"original_amount_minor"`
	OriginalRegion        string  `json:"original_region,omitempty"`
	Section               string  `json:"section"`
	LineKind              string  `json:"line_kind"`
	AccountingTreatment   string  `json:"accounting_treatment"`
	SettlesPriorStatement bool    `json:"settles_prior_statement"`
	RequiresExpenseReview bool    `json:"requires_expense_review"`
	LineHash              string  `json:"line_hash"`
}

// ParseCMBCreditCardPDF extracts text locally from a text-based CMB credit-card
// statement. Scanned/image-only documents deliberately return an error so they
// can be routed to OCR or vision instead of silently producing partial data.
func ParseCMBCreditCardPDF(readerAt io.ReaderAt, size int64) (CreditCardStatement, error) {
	reader, err := pdf.NewReader(readerAt, size)
	if err != nil {
		return CreditCardStatement{}, fmt.Errorf("open PDF: %w", err)
	}

	pages := make([][]string, 0, reader.NumPage())
	for pageNumber := 1; pageNumber <= reader.NumPage(); pageNumber++ {
		rows, rowErr := reader.Page(pageNumber).GetTextByRow()
		if rowErr != nil {
			return CreditCardStatement{}, fmt.Errorf("extract page %d: %w", pageNumber, rowErr)
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

	statement, err := ParseCMBCreditCardRows(pages)
	if err != nil {
		return CreditCardStatement{}, err
	}

	hash := sha256.New()
	section := io.NewSectionReader(readerAt, 0, size)
	if _, err := io.Copy(hash, section); err != nil {
		return CreditCardStatement{}, fmt.Errorf("hash PDF: %w", err)
	}
	statement.ArtifactSHA256 = hex.EncodeToString(hash.Sum(nil))
	return statement, nil
}

func joinPDFRow(content pdf.TextHorizontal) string {
	var builder strings.Builder
	var previous *pdf.Text
	for index := range content {
		current := content[index]
		value := strings.TrimSpace(current.S)
		if value == "" {
			continue
		}
		if previous != nil {
			gap := current.X - (previous.X + previous.W)
			if gap > 1.5 {
				builder.WriteByte(' ')
			}
		}
		builder.WriteString(value)
		copy := current
		previous = &copy
	}
	return normalizeSpaces(builder.String())
}

// ParseCMBCreditCardText is useful for deterministic tests and for a future
// OCR/vision fallback. It expects one visual table row per line.
func ParseCMBCreditCardText(text string) (CreditCardStatement, error) {
	return ParseCMBCreditCardRows([][]string{strings.Split(text, "\n")})
}

func ParseCMBCreditCardRows(pages [][]string) (CreditCardStatement, error) {
	allRows := make([]string, 0)
	for _, page := range pages {
		for _, row := range page {
			row = normalizeSpaces(row)
			if row != "" {
				allRows = append(allRows, row)
			}
		}
	}
	allText := strings.Join(allRows, "\n")
	if !strings.Contains(allText, cmbStatementMarker) {
		return CreditCardStatement{}, errors.New("document is not a supported CMB credit-card statement")
	}

	statementDate, err := findDateAfterLabel(allRows, "账单日")
	if err != nil {
		return CreditCardStatement{}, err
	}
	dueDate, err := findDateAfterLabel(allRows, "到期还款日")
	if err != nil {
		return CreditCardStatement{}, err
	}

	statement := CreditCardStatement{
		Provider:             "CMB",
		StatementType:        "CREDIT_CARD",
		Currency:             "CNY",
		StatementDate:        statementDate.Format("2006-01-02"),
		PaymentDueDate:       dueDate.Format("2006-01-02"),
		StatementPeriodStart: previousCycleStart(statementDate).Format("2006-01-02"),
		StatementPeriodEnd:   statementDate.Format("2006-01-02"),
		PeriodDerivation:     "PREVIOUS_STATEMENT_DATE_PLUS_ONE_DAY",
		ValidationErrors:     make([]string, 0),
		UnrecognizedRows:     make([]string, 0),
		Lines:                make([]CreditCardLine, 0),
	}

	statement.CreditLimitMinor, _ = findMoneyAfterLabel(allRows, "信用额度")
	statement.ClosingBalanceMinor, _ = findMoneyAfterLabel(allRows, "本期应还金额")
	statement.MinimumPaymentMinor, _ = findMoneyAfterLabel(allRows, "本期最低还款额")

	summary, summaryFound := findSummaryAmounts(allRows)
	if summaryFound {
		statement.ClosingBalanceMinor = summary[0]
		statement.OpeningBalanceMinor = summary[1]
		statement.PreviousPaymentsMinor = summary[2]
		statement.NewChargesMinor = summary[3]
		statement.AdjustmentsMinor = summary[4]
		statement.InterestMinor = summary[5]
	}

	section := ""
	var observedStart time.Time
	for _, row := range allRows {
		// Some CMB PDFs place the repeating page header at nearly the same Y
		// coordinate as the final table row. The PDF text layer then exposes
		// both visual rows as one string; keep the transaction portion.
		if markerIndex := strings.Index(row, cmbStatementMarker); markerIndex > 0 {
			row = strings.TrimSpace(row[:markerIndex])
		}
		if recognized := sectionName(row); recognized != "" {
			section = recognized
			continue
		}
		matches := transactionPattern.FindStringSubmatch(row)
		if len(matches) == 0 {
			if section != "" && transactionStartPattern.MatchString(row) {
				statement.UnrecognizedRows = append(statement.UnrecognizedRows, row)
			}
			continue
		}
		if section == "" {
			continue
		}
		line, parseErr := parseTransaction(matches, section, statementDate, len(statement.Lines)+1)
		if parseErr != nil {
			statement.ValidationErrors = append(statement.ValidationErrors, parseErr.Error())
			continue
		}
		statement.Lines = append(statement.Lines, line)
		posted, _ := time.Parse("2006-01-02", line.PostedDate)
		if observedStart.IsZero() || posted.Before(observedStart) {
			observedStart = posted
		}
		if line.Direction == "DEBIT" {
			statement.TotalDebitMinor += line.AmountMinor
		} else {
			statement.TotalCreditMinor += line.AmountMinor
			switch line.LineKind {
			case LineKindRepayment:
				statement.RepaymentCreditMinor += line.AmountMinor
			case LineKindCashback:
				statement.CashbackCreditMinor += line.AmountMinor
			default:
				statement.NonRepaymentCreditMinor += line.AmountMinor
			}
		}
	}
	if !observedStart.IsZero() {
		statement.ObservedPostingStart = observedStart.Format("2006-01-02")
	}
	if len(statement.Lines) == 0 {
		return CreditCardStatement{}, errors.New("no transaction rows were recognized")
	}
	if len(statement.UnrecognizedRows) > 0 {
		statement.ValidationErrors = append(
			statement.ValidationErrors,
			fmt.Sprintf("%d transaction-like rows were not recognized", len(statement.UnrecognizedRows)),
		)
	}

	expectedClosing := statement.OpeningBalanceMinor +
		statement.TotalDebitMinor - statement.TotalCreditMinor
	statement.BalanceValid = expectedClosing == statement.ClosingBalanceMinor
	if !statement.BalanceValid {
		statement.ValidationErrors = append(
			statement.ValidationErrors,
			fmt.Sprintf(
				"balance mismatch: opening %d + debits %d - credits %d != closing %d",
				statement.OpeningBalanceMinor,
				statement.TotalDebitMinor,
				statement.TotalCreditMinor,
				statement.ClosingBalanceMinor,
			),
		)
	}

	statement.SummaryValid = summaryFound &&
		statement.PreviousPaymentsMinor ==
			statement.RepaymentCreditMinor+statement.CashbackCreditMinor &&
		statement.NewChargesMinor == statement.TotalDebitMinor &&
		statement.AdjustmentsMinor == statement.NonRepaymentCreditMinor &&
		statement.ClosingBalanceMinor == statement.OpeningBalanceMinor-
			statement.PreviousPaymentsMinor+statement.NewChargesMinor-
			statement.AdjustmentsMinor+statement.InterestMinor
	if !statement.SummaryValid {
		statement.ValidationErrors = append(statement.ValidationErrors, "statement summary does not reconcile with parsed transaction classes")
	}
	statement.NeedsReview = len(statement.ValidationErrors) > 0
	return statement, nil
}

func parseTransaction(
	matches []string,
	section string,
	statementDate time.Time,
	lineNumber int,
) (CreditCardLine, error) {
	firstDate := matches[1]
	secondDate := matches[2]
	description := strings.TrimSpace(matches[3])
	signedAmount, err := parseMoney(matches[4])
	if err != nil {
		return CreditCardLine{}, fmt.Errorf("line %d amount: %w", lineNumber, err)
	}
	originalAmount, err := parseMoney(matches[6])
	if err != nil {
		return CreditCardLine{}, fmt.Errorf("line %d original amount: %w", lineNumber, err)
	}

	postedValue := firstDate
	var transactionValue *string
	if secondDate != "" {
		postedValue = secondDate
		transactionValue = &firstDate
	}
	postedDate, err := resolveMonthDay(postedValue, statementDate)
	if err != nil {
		return CreditCardLine{}, fmt.Errorf("line %d posted date: %w", lineNumber, err)
	}
	var transactionDate *string
	if transactionValue != nil {
		resolved, resolveErr := resolveMonthDay(*transactionValue, postedDate)
		if resolveErr != nil {
			return CreditCardLine{}, fmt.Errorf("line %d transaction date: %w", lineNumber, resolveErr)
		}
		value := resolved.Format("2006-01-02")
		transactionDate = &value
	}

	direction := "DEBIT"
	absoluteAmount := signedAmount
	if signedAmount < 0 {
		direction = "CREDIT"
		absoluteAmount = -signedAmount
	}
	lineKind, treatment, prior, review := classifyLine(section, description, signedAmount)
	hashInput := fmt.Sprintf(
		"%d|%s|%s|%s|%d|%s|%s",
		lineNumber,
		postedDate.Format("2006-01-02"),
		valueOrEmpty(transactionDate),
		description,
		signedAmount,
		matches[5],
		lineKind,
	)
	sum := sha256.Sum256([]byte(hashInput))
	return CreditCardLine{
		LineNumber:            lineNumber,
		TransactionDate:       transactionDate,
		PostedDate:            postedDate.Format("2006-01-02"),
		Description:           description,
		AmountMinor:           absoluteAmount,
		SignedAmountMinor:     signedAmount,
		Direction:             direction,
		CardLastFour:          matches[5],
		OriginalAmountMinor:   originalAmount,
		OriginalRegion:        matches[7],
		Section:               section,
		LineKind:              lineKind,
		AccountingTreatment:   treatment,
		SettlesPriorStatement: prior,
		RequiresExpenseReview: review,
		LineHash:              hex.EncodeToString(sum[:]),
	}, nil
}

func classifyLine(section, description string, signedAmount int64) (string, string, bool, bool) {
	// CMB places card-program cashback in the statement's "Payment" summary,
	// even though it is not a repayment and must not settle a prior statement.
	// Detect it by description as well as section because older statement
	// layouts may append the "其他" rows to the preceding PDF text section.
	if signedAmount < 0 && strings.Contains(description, "返现") {
		return LineKindCashback, "CARD_CASHBACK", false, false
	}

	switch section {
	case "还款":
		return LineKindRepayment, "TRANSFER_TO_CREDIT_CARD", true, false
	case "退款":
		return LineKindRefund, "REFUND_MATCH_CANDIDATE", false, true
	case "分期":
		switch {
		case strings.Contains(description, "分期办理"):
			return LineKindInstallmentSetup, "ACCOUNT_ADJUSTMENT", false, true
		case strings.Contains(description, "财政贴息"):
			return LineKindInterestSubsidy, "ACCOUNT_ADJUSTMENT", false, false
		case strings.Contains(description, "分期利息"):
			return LineKindInstallmentInterest, "FINANCE_EXPENSE", false, false
		case strings.Contains(description, "本金"):
			return LineKindInstallmentPrincipal, "INSTALLMENT_PRINCIPAL_REVIEW", false, true
		default:
			return LineKindAdjustment, "ACCOUNT_ADJUSTMENT", false, true
		}
	case "消费":
		return LineKindPurchase, "EXPENSE_CANDIDATE", false, false
	case "其他":
		return LineKindAdjustment, "ACCOUNT_ADJUSTMENT", false, true
	default:
		if signedAmount < 0 {
			return LineKindAdjustment, "ACCOUNT_ADJUSTMENT", false, true
		}
		return LineKindPurchase, "EXPENSE_CANDIDATE", false, true
	}
}

func sectionName(row string) string {
	row = strings.TrimSpace(row)
	switch row {
	case "还款", "分期", "退款", "消费", "其他":
		return row
	default:
		return ""
	}
}

func findDateAfterLabel(rows []string, label string) (time.Time, error) {
	for index, row := range rows {
		if !strings.Contains(row, label) {
			continue
		}
		for offset := 0; offset <= 3 && index+offset < len(rows); offset++ {
			if value := dateCNPattern.FindString(rows[index+offset]); value != "" {
				return time.Parse("2006年01月02日", value)
			}
		}
	}
	return time.Time{}, fmt.Errorf("%s not found", label)
}

func findMoneyAfterLabel(rows []string, label string) (int64, error) {
	for index, row := range rows {
		if !strings.Contains(row, label) {
			continue
		}
		for offset := 0; offset <= 3 && index+offset < len(rows); offset++ {
			match := moneyPattern.FindStringSubmatch(rows[index+offset])
			if len(match) > 1 {
				return parseMoney(match[1])
			}
		}
	}
	return 0, fmt.Errorf("%s amount not found", label)
}

func findSummaryAmounts(rows []string) ([6]int64, bool) {
	var result [6]int64
	for index, row := range rows {
		if !strings.Contains(row, "Balance B/F") {
			continue
		}
		for offset := 0; offset <= 5 && index+offset < len(rows); offset++ {
			values := moneyPattern.FindAllStringSubmatch(rows[index+offset], -1)
			if len(values) < 6 {
				continue
			}
			for valueIndex := 0; valueIndex < 6; valueIndex++ {
				amount, err := parseMoney(values[valueIndex][1])
				if err != nil {
					return [6]int64{}, false
				}
				result[valueIndex] = amount
			}
			return result, true
		}
	}
	return [6]int64{}, false
}

func parseMoney(value string) (int64, error) {
	value = strings.ReplaceAll(strings.TrimSpace(value), ",", "")
	negative := strings.HasPrefix(value, "-")
	value = strings.TrimPrefix(value, "-")
	parts := strings.Split(value, ".")
	if len(parts) != 2 || len(parts[1]) != 2 {
		return 0, fmt.Errorf("invalid money %q", value)
	}
	major, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, err
	}
	minor, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, err
	}
	result := major*100 + minor
	if negative {
		result = -result
	}
	return result, nil
}

func resolveMonthDay(value string, notAfter time.Time) (time.Time, error) {
	parts := strings.Split(value, "/")
	if len(parts) != 2 {
		return time.Time{}, fmt.Errorf("invalid month/day %q", value)
	}
	month, monthErr := strconv.Atoi(parts[0])
	day, dayErr := strconv.Atoi(parts[1])
	if monthErr != nil || dayErr != nil {
		return time.Time{}, fmt.Errorf("invalid month/day %q", value)
	}
	result := time.Date(notAfter.Year(), time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if result.Month() != time.Month(month) || result.Day() != day {
		return time.Time{}, fmt.Errorf("invalid calendar date %q", value)
	}
	if result.After(notAfter) {
		result = time.Date(notAfter.Year()-1, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	}
	return result, nil
}

func previousCycleStart(statementDate time.Time) time.Time {
	year, month := statementDate.Year(), statementDate.Month()-1
	if month == 0 {
		year--
		month = 12
	}
	day := statementDate.Day()
	lastDay := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
}

func normalizeSpaces(value string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(value, "\u00a0", " ")), " ")
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
