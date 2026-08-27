package statementparser

import "testing"

func TestParseCMBSavingsText(t *testing.T) {
	statement, err := ParseCMBSavingsText(`招商银行交易流水
2024-01-01 -- 2024-01-31
户 名： 测试用户 账号：6222********0000
2024-01-01 CNY 22.00 467.08 银联代付 测试用户
2024-01-02 CNY -6.30 460.78 快捷支付 微信转账
2024-01-02 CNY -30.00 430.78 银联快捷支付 微信转账
2024-01-02 CNY 36.30 467.08 银联代付 支付宝支付科技有限公司
2024-01-27 CNY 900.00 1367.08 代发住房公积金 测试公积金中心`)
	if err != nil {
		t.Fatalf("parse savings statement: %v", err)
	}
	if statement.Provider != DebitStatementProvider || statement.StatementType != "SAVINGS_ACCOUNT" {
		t.Fatalf("unexpected statement identity: %+v", statement)
	}
	if statement.AccountHolder != "测试用户" || statement.StatementPeriodStart != "2024-01-01" || statement.StatementPeriodEnd != "2024-01-31" {
		t.Fatalf("unexpected statement metadata: %+v", statement)
	}
	if len(statement.Lines) != 5 || !statement.BalanceValid || !statement.SummaryValid {
		t.Fatalf("unexpected validation result: lines=%d balance=%v summary=%v errors=%v", len(statement.Lines), statement.BalanceValid, statement.SummaryValid, statement.ValidationErrors)
	}
	if statement.OpeningBalanceMinor != 44508 || statement.ClosingBalanceMinor != 136708 {
		t.Fatalf("unexpected balances: opening=%d closing=%d", statement.OpeningBalanceMinor, statement.ClosingBalanceMinor)
	}
	if statement.Lines[0].CounterpartyType != "PERSON" || !statement.Lines[0].RequiresManualReview {
		t.Fatalf("personal counterparty was not forced to review: %+v", statement.Lines[0])
	}
	if statement.Lines[1].MerchantChannel != "WECHAT" || statement.Lines[2].MerchantChannel != "WECHAT" {
		t.Fatalf("WeChat settlement channel was not detected")
	}
	if statement.Lines[3].MerchantChannel != "ALIPAY" || statement.Lines[3].CounterpartyType != "ORGANIZATION" {
		t.Fatalf("Alipay organization counterparty was not classified: %+v", statement.Lines[3])
	}
	if statement.Lines[4].Description != "代发住房公积金" || statement.Lines[4].LineKind != LineKindIncome {
		t.Fatalf("income summary was not parsed: %+v", statement.Lines[4])
	}
}

func TestParseCMBSavingsRejectsOtherDocument(t *testing.T) {
	if _, err := ParseCMBSavingsText("招商银行信用卡账单\n2024-01-01 -- 2024-01-31"); err == nil {
		t.Fatal("expected a non-savings statement to be rejected")
	}
}

func TestCMBAutoHandledShortcutLinesDoNotRequireManualReview(t *testing.T) {
	statement, err := ParseCMBSavingsText(`招商银行交易流水
2024-01-01 -- 2024-01-31
户 名： 测试用户 账号：6222********0000
2024-01-01 CNY -40.42 59.58 快捷支付 张三
2024-01-25 CNY 1.00 60.58 快捷退款 李四
2024-01-26 CNY -10.00 50.58 银联快捷支付 王五`)
	if err != nil {
		t.Fatalf("parse shortcut lines: %v", err)
	}
	for index, line := range statement.Lines {
		if line.CounterpartyType != "PERSON" {
			t.Fatalf("expected sample counterparty to remain recognized as a person: %+v", line)
		}
		if index < 2 && line.RequiresManualReview {
			t.Fatalf("shortcut line should not require manual review: %+v", line)
		}
		if index < 2 && line.AccountingTreatment != "PLATFORM_SETTLEMENT_EXCLUDED" {
			t.Fatalf("shortcut line should be excluded as platform settlement: %+v", line)
		}
		if index == 2 && !line.RequiresManualReview {
			t.Fatalf("UnionPay shortcut line should remain manual for now: %+v", line)
		}
	}
}
