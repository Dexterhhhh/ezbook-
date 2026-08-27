package api

import (
	"testing"

	"github.com/mayswind/ezbookkeeping/pkg/hengcai"
	"github.com/mayswind/ezbookkeeping/pkg/hengcai/statementparser"
)

func TestParseWeChatExpenseAndRefund(t *testing.T) {
	data := []byte("微信支付账单明细\n----------------------微信支付账单明细列表--------------------\n交易时间,交易类型,交易对方,商品,收/支,金额(元),支付方式,当前状态,交易单号,商家单号,备注\n2026-08-02 12:00:00,商户消费,咖啡店,拿铁,支出,¥28.50,招行,支付成功,wx-1,m-1,/\n2026-08-03 12:00:00,商户消费,咖啡店,退款,收入,¥28.50,招行,已退款,wx-2,m-2,/\n")
	lines, err := parseExpenseCSV("WECHAT", data)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if lines[0].AmountMinor != 2850 || lines[0].Kind != "PURCHASE" {
		t.Fatalf("unexpected purchase: %#v", lines[0])
	}
	if lines[1].AmountMinor != -2850 || lines[1].Kind != "REFUND" {
		t.Fatalf("refund must be negative expense: %#v", lines[1])
	}
}

func TestPlatformChannel(t *testing.T) {
	cases := map[string]string{"支付宝-杭州某商户": "ALIPAY", "财付通-微信支付": "WECHAT", "TENPAY SHOP": "WECHAT", "ordinary merchant": ""}
	for input, expected := range cases {
		if actual := platformChannel(input); actual != expected {
			t.Fatalf("%q: expected %q, got %q", input, expected, actual)
		}
	}
}

func TestCmbSavingsPlatformSettlementScope(t *testing.T) {
	if !isCmbSavingsPlatformSettlement("快捷支付") || !isCmbSavingsPlatformSettlement("快捷退款") {
		t.Fatal("快捷支付和快捷退款应直接按平台结算流水剔除")
	}
	if isCmbSavingsPlatformSettlement("银联快捷支付") {
		t.Fatal("银联快捷支付暂不应按平台结算流水剔除")
	}
}

func TestUnionPayReviewOnlyRestoredBeforeManualDecision(t *testing.T) {
	base := hengcai.StatementLine{Status: "UNMATCHED", CounterpartyType: "PERSON", Description: "银联快捷支付", LineKind: statementparser.LineKindPurchase}
	if !shouldRestoreUnionPayReview(&base) {
		t.Fatal("unconfirmed UnionPay purchase should retain the manual review gate")
	}
	manual := base
	manual.Classification, manual.CategoryId = "人工确认", 1
	if shouldRestoreUnionPayReview(&manual) {
		t.Fatal("manual classification must not be put back into review")
	}
	refund := base
	refund.LineKind, refund.Classification = statementparser.LineKindRefund, "人工确认退款"
	if shouldRestoreUnionPayReview(&refund) {
		t.Fatal("confirmed refund must not be put back into review")
	}
}

func TestManualClassificationRepairClearsStaleAmbiguityOnly(t *testing.T) {
	line := &hengcai.StatementLine{Status: "REVIEW", MatchType: "AMBIGUOUS", CounterpartyType: "PERSON", Classification: "人工确认", CategoryId: 1, LineKind: statementparser.LineKindPurchase}
	if !shouldRepairManualClassification(line) {
		t.Fatal("manual category should resolve a stale ambiguous person match")
	}
	line.MatchType = "PLATFORM_UNRESOLVED"
	if shouldRepairManualClassification(line) {
		t.Fatal("manual category must not bypass unresolved platform duplicate protection")
	}
	line.MatchType, line.Status = "AMBIGUOUS", "EVIDENCE"
	if shouldRepairManualClassification(line) {
		t.Fatal("finalized evidence must not be converted into a ledger classification")
	}
	refund := &hengcai.StatementLine{Status: "REVIEW", MatchType: "PERSON_COUNTERPARTY", CounterpartyType: "PERSON", Classification: "人工确认退款", CategoryId: 1, LineKind: statementparser.LineKindRefund}
	if !shouldRepairManualClassification(refund) {
		t.Fatal("confirmed refund should resolve a stale person review gate")
	}
}

func TestFundingSourceInference(t *testing.T) {
	if got := inferFundingSource("招行信用卡", "咖啡"); got != "CREDIT_CARD" {
		t.Fatalf("信用卡支付方式应识别为 CREDIT_CARD，实际为 %s", got)
	}
	if got := inferFundingSource("银行卡", "超市"); got != "BANK_ACCOUNT" {
		t.Fatalf("银行支付方式应识别为 BANK_ACCOUNT，实际为 %s", got)
	}
}

func TestDateDistance(t *testing.T) {
	if dateDistanceDays("2026-08-01", "2026-08-03") != 2 {
		t.Fatal("date distance should be absolute")
	}
	if dateDistanceDays("bad", "2026-08-03") != 999 {
		t.Fatal("invalid dates must never auto-match")
	}
}

func TestVerifiedPlatformCoverage(t *testing.T) {
	statements := []hengcai.StatementImport{
		{Provider: "ALIPAY", Status: "POSTED", CoverageStatus: "VERIFIED", CoverageDimension: "MERCHANT_CHANNEL", PeriodStart: "2026-07-01", PeriodEnd: "2026-07-31"},
		{Provider: "WECHAT", Status: "REVIEW", CoverageStatus: "PENDING", CoverageDimension: "MERCHANT_CHANNEL", PeriodStart: "2026-07-01", PeriodEnd: "2026-07-31"},
	}
	if !hasVerifiedPlatformCoverage(statements, "ALIPAY", "2026-07-30") {
		t.Fatal("a posted platform statement should cover transactions within its period")
	}
	if hasVerifiedPlatformCoverage(statements, "ALIPAY", "2026-08-01") {
		t.Fatal("platform coverage must not extend past the statement period")
	}
	if hasVerifiedPlatformCoverage(statements, "WECHAT", "2026-07-30") {
		t.Fatal("a pending platform statement must not suppress bank transactions")
	}
	if hasVerifiedPlatformCoverage(statements, "ALIPAY", "") {
		t.Fatal("an invalid transaction date must not be considered covered")
	}
}

func TestPlatformCoverageUsesTransactionDate(t *testing.T) {
	line := &hengcai.StatementLine{TransactionDate: "2026-07-31", PostedDate: "2026-08-01"}
	if got := platformCoverageDate(line); got != "2026-07-31" {
		t.Fatalf("expected transaction date, got %s", got)
	}
	line.TransactionDate = "invalid"
	if got := platformCoverageDate(line); got != "2026-08-01" {
		t.Fatalf("expected posted-date fallback, got %s", got)
	}
}

func TestStatementOverlapsMonth(t *testing.T) {
	statement := &hengcai.StatementImport{PeriodStart: "2026-07-24", PeriodEnd: "2026-08-23"}
	if !statementOverlapsMonth(statement, "2026-07") || !statementOverlapsMonth(statement, "2026-08") {
		t.Fatal("a cross-month billing cycle should be actionable from every covered month")
	}
	if statementOverlapsMonth(statement, "2026-06") || statementOverlapsMonth(statement, "2026-09") {
		t.Fatal("a billing cycle must not be actionable outside its covered period")
	}
	if statementOverlapsMonth(statement, "invalid") {
		t.Fatal("an invalid view month must not match a statement")
	}
}

func TestLegacyStatementOverlapFallsBackToStatementMonth(t *testing.T) {
	statement := &hengcai.StatementImport{PeriodEnd: "2026-08-31"}
	if !statementOverlapsMonth(statement, "2026-08") || statementOverlapsMonth(statement, "2026-07") {
		t.Fatal("a legacy statement without a complete period should belong to its period-end month")
	}
}
