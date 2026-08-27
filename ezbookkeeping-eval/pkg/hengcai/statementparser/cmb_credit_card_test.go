package statementparser

import (
	"testing"
	"time"
)

func TestParseCMBCreditCardTextUsesStatementCycleAndClassifiesRepayment(t *testing.T) {
	t.Parallel()

	input := `
招商银行信用卡对账单（个人消费卡账户 2026年06月）
账单日 2026年06月23日
信用额度 ¥ 20,000.00
到期还款日 2026年07月11日
本期应还金额 ¥ 1,230.00
本期最低还款额 ¥ 200.00
还款
05/24 掌上生活还款 -500.00 1234 -500.00
分期
01/10 06/11 分期还款 本金 第2/12期 100.00 1234 100.00
01/10 06/11 分期还款 分期利息 第2/12期 5.00 1234 5.00
消费
05/24 05/25 示例商户 1,625.00 1234 1,625.00(CN)招商银行信用卡对账单（个人消费卡账户 2026年06月）
本期应还金额 Balance B/F Payment New Charges Adjustment Interest
¥ 1,230.00 ¥ 0.00 ¥ 500.00 ¥ 1,730.00 ¥ 0.00 ¥ 0.00
`
	statement, err := ParseCMBCreditCardText(input)
	if err != nil {
		t.Fatalf("parse statement: %v", err)
	}
	if statement.StatementPeriodStart != "2026-05-24" ||
		statement.StatementPeriodEnd != "2026-06-23" {
		t.Fatalf(
			"unexpected cycle %s..%s",
			statement.StatementPeriodStart,
			statement.StatementPeriodEnd,
		)
	}
	if len(statement.Lines) != 4 {
		t.Fatalf("got %d lines, want 4", len(statement.Lines))
	}
	repayment := statement.Lines[0]
	if repayment.LineKind != LineKindRepayment ||
		!repayment.SettlesPriorStatement ||
		repayment.AccountingTreatment != "TRANSFER_TO_CREDIT_CARD" {
		t.Fatalf("repayment was misclassified: %#v", repayment)
	}
	if repayment.TransactionDate != nil || repayment.PostedDate != "2026-05-24" {
		t.Fatalf("unexpected repayment dates: %#v", repayment)
	}
	if !statement.BalanceValid || !statement.SummaryValid || statement.NeedsReview {
		t.Fatalf("valid statement needs review: %#v", statement.ValidationErrors)
	}
}

func TestResolveInstallmentTransactionDateAcrossYear(t *testing.T) {
	t.Parallel()

	statement, err := ParseCMBCreditCardText(`
招商银行信用卡对账单
账单日 2026年06月23日
到期还款日 2026年07月11日
本期应还金额 ¥ 999.91
分期
09/12 06/13 消费分期-示例商城 本金 第10/12期 999.91 4401 999.91
本期应还金额 Balance B/F Payment New Charges Adjustment Interest
¥ 999.91 ¥ 0.00 ¥ 0.00 ¥ 999.91 ¥ 0.00 ¥ 0.00
`)
	if err != nil {
		t.Fatalf("parse statement: %v", err)
	}
	if statement.Lines[0].TransactionDate == nil ||
		*statement.Lines[0].TransactionDate != "2025-09-12" {
		t.Fatalf("installment date was not rolled back a year: %#v", statement.Lines[0])
	}
	if !statement.Lines[0].RequiresExpenseReview {
		t.Fatal("installment principal must require expense review")
	}
}

func TestParseCMBCreditCardTextTreatsCashbackAsCMBPayment(t *testing.T) {
	t.Parallel()

	statement, err := ParseCMBCreditCardText(`
招商银行信用卡对账单
账单日 2026年04月23日
到期还款日 2026年05月11日
本期应还金额 ¥ 30.22
还款
04/02 掌上生活还款 -100.00 8428 -100.00
消费
04/05 04/06 示例商户 50.00 8428 50.00(CN)
其他
04/01 04/02 招行银联卡境外消费1%返现 -11.62 4401 -11.62(HK)
04/09 04/10 招行银联卡境外消费1%返现 -8.16 4401 -8.16(HK)
本期应还金额 Balance B/F Payment New Charges Adjustment Interest
¥ 30.22 ¥ 100.00 ¥ 119.78 ¥ 50.00 ¥ 0.00 ¥ 0.00
`)
	if err != nil {
		t.Fatalf("parse statement: %v", err)
	}
	if statement.RepaymentCreditMinor != 10_000 {
		t.Fatalf("repayment credits = %d, want 10000", statement.RepaymentCreditMinor)
	}
	if statement.CashbackCreditMinor != 1_978 {
		t.Fatalf("cashback credits = %d, want 1978", statement.CashbackCreditMinor)
	}
	if statement.NonRepaymentCreditMinor != 0 {
		t.Fatalf("non-payment adjustments = %d, want 0", statement.NonRepaymentCreditMinor)
	}
	for _, line := range statement.Lines[2:] {
		if line.LineKind != LineKindCashback ||
			line.AccountingTreatment != "CARD_CASHBACK" ||
			line.SettlesPriorStatement ||
			line.RequiresExpenseReview {
			t.Fatalf("cashback was misclassified: %#v", line)
		}
	}
	if !statement.BalanceValid || !statement.SummaryValid || statement.NeedsReview {
		t.Fatalf("cashback statement needs review: %#v", statement.ValidationErrors)
	}
}

func TestParseTransactionHashIncludesLineNumber(t *testing.T) {
	matches := transactionPattern.FindStringSubmatch("05/24 05/25 示例商户 1,625.00 1234 1,625.00(CN)")
	if matches == nil {
		t.Fatal("test transaction did not match parser pattern")
	}
	statementDate := time.Date(2026, time.June, 23, 0, 0, 0, 0, time.UTC)
	first, err := parseTransaction(matches, "消费", statementDate, 1)
	if err != nil {
		t.Fatalf("parse first transaction: %v", err)
	}
	second, err := parseTransaction(matches, "消费", statementDate, 2)
	if err != nil {
		t.Fatalf("parse second transaction: %v", err)
	}
	if first.LineHash == second.LineHash {
		t.Fatal("duplicate transactions must have distinct line hashes")
	}
}
