package api

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/mayswind/ezbookkeeping/pkg/hengcai"
	"github.com/mayswind/ezbookkeeping/pkg/models"
	"github.com/mayswind/ezbookkeeping/pkg/utils"
	"xorm.io/xorm"
)

func TestPostableCategoriesExcludesPrimaryHiddenAndNil(t *testing.T) {
	categories := []*models.TransactionCategory{
		nil,
		{CategoryId: 1, ParentCategoryId: models.LevelOneTransactionCategoryParentId, Name: "primary"},
		{CategoryId: 2, ParentCategoryId: 1, Name: "hidden", Hidden: true},
		{CategoryId: 3, ParentCategoryId: 1, Name: "leaf"},
		{CategoryId: 4, ParentCategoryId: models.LevelOneTransactionCategoryParentId, Name: "hidden primary", Hidden: true},
		{CategoryId: 5, ParentCategoryId: 4, Name: "leaf under hidden primary"},
	}
	actual := postableCategories(categories)
	if len(actual) != 1 || actual[0].CategoryId != 3 {
		t.Fatalf("unexpected postable categories: %#v", actual)
	}
}

func TestStatementTransactionTimeUsesTransactionSequenceFormat(t *testing.T) {
	actual, err := statementTransactionTime("2026-07-01")
	if err != nil {
		t.Fatal(err)
	}
	expectedUnix := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC).Unix()
	if utils.GetUnixTimeFromTransactionTime(actual) != expectedUnix {
		t.Fatalf("transaction time %d resolves to %d, want %d", actual, utils.GetUnixTimeFromTransactionTime(actual), expectedUnix)
	}
}

func TestAlpacaSecretRoundTrip(t *testing.T) {
	sealed, err := sealAlpacaSecret("local-test-secret", "alpaca-secret")
	if err != nil {
		t.Fatal(err)
	}
	if sealed == "alpaca-secret" {
		t.Fatal("secret was not sealed")
	}
	plain, err := openAlpacaSecret("local-test-secret", sealed)
	if err != nil {
		t.Fatal(err)
	}
	if plain != "alpaca-secret" {
		t.Fatalf("got %q", plain)
	}
}

func TestAddMonthsToDateClampsMonthEnd(t *testing.T) {
	actual, err := addMonthsToDate("2026-01-31", 1)
	if err != nil {
		t.Fatal(err)
	}
	if actual != "2026-02-28" {
		t.Fatalf("got %s", actual)
	}
	actual, err = addMonthsToDate("2026-01-31", 2)
	if err != nil {
		t.Fatal(err)
	}
	if actual != "2026-03-31" {
		t.Fatalf("got %s", actual)
	}
}

func TestSplitAmountPreservesTotal(t *testing.T) {
	total := int64(10001)
	allocated := int64(0)
	for i := 0; i < 3; i++ {
		allocated += splitAmount(total, 3, i)
	}
	if allocated != total {
		t.Fatalf("allocated %d, want %d", allocated, total)
	}
}

func TestCapexInstallmentCashflowExcludesInterestAndFees(t *testing.T) {
	item := &hengcai.CapexInstallment{PrincipalMinor: 10000, InterestMinor: 500, FeeMinor: 200, Status: "SCHEDULED"}
	if actual := capexInstallmentPrincipalCashflow(item); actual != 10000 {
		t.Fatalf("scheduled CAPEX cashflow=%d, want principal only", actual)
	}
	item.Status = "PAID"
	item.ActualPaidMinor = 10700
	if actual := capexInstallmentPrincipalCashflow(item); actual != 10000 {
		t.Fatalf("paid CAPEX cashflow=%d, want principal capped at 10000", actual)
	}
	item.ActualPaidMinor = 6000
	if actual := capexInstallmentPrincipalCashflow(item); actual != 6000 {
		t.Fatalf("partial CAPEX cashflow=%d, want actual principal 6000", actual)
	}
}

func TestForecastIncomeUsesSalaryAndPerformanceCadences(t *testing.T) {
	setting := &hengcai.IncomeForecastSetting{MonthlySalaryMinor: 2000000, MonthlyPerformanceMinor: 100000, QuarterlyPerformanceMinor: 300000, AnnualPerformanceMinor: 5000000, PerformanceMonth: 12}
	regular, err := forecastIncomeForMonth(setting, "2026-11")
	if err != nil || regular != 2100000 {
		t.Fatalf("regular month income=%d err=%v", regular, err)
	}
	quarterly, err := forecastIncomeForMonth(setting, "2026-09")
	if err != nil || quarterly != 2400000 {
		t.Fatalf("quarter-end income=%d err=%v", quarterly, err)
	}
	performance, err := forecastIncomeForMonth(setting, "2026-12")
	if err != nil || performance != 7400000 {
		t.Fatalf("performance month income=%d err=%v", performance, err)
	}
}

func TestRestoredStatementLineState(t *testing.T) {
	tests := []struct {
		name          string
		line          *hengcai.StatementLine
		created       bool
		wantStatus    string
		wantMatchedID int64
	}{
		{name: "created classified transaction", line: &hengcai.StatementLine{CategoryId: 12, MatchedTransactionId: 99}, created: true, wantStatus: "CLASSIFIED"},
		{name: "existing matched transaction", line: &hengcai.StatementLine{CategoryId: 12, MatchedTransactionId: 99}, wantStatus: "MATCHED", wantMatchedID: 99},
		{name: "unclassified line", line: &hengcai.StatementLine{}, created: true, wantStatus: "UNMATCHED"},
		{name: "capex principal", line: &hengcai.StatementLine{LineKind: "INSTALLMENT_PRINCIPAL", MatchedTransactionId: 99}, wantStatus: "CAPEX_LINKED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status, matchedID := restoredStatementLineState(test.line, test.created)
			if status != test.wantStatus || matchedID != test.wantMatchedID {
				t.Fatalf("got (%s,%d), want (%s,%d)", status, matchedID, test.wantStatus, test.wantMatchedID)
			}
		})
	}
}

func TestReverseStatementCreatedTransactionRestoresAccountBalance(t *testing.T) {
	engine, err := xorm.NewEngine("sqlite3", filepath.Join(t.TempDir(), "reverse.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer engine.Close()
	if err = engine.Sync2(new(models.Account), new(models.Transaction), new(models.TransactionTagIndex), new(models.TransactionPictureInfo)); err != nil {
		t.Fatal(err)
	}
	account := &models.Account{AccountId: 10, Uid: 1, Name: "test", Currency: "CNY", Balance: 12000}
	transaction := &models.Transaction{TransactionId: 20, Uid: 1, Type: models.TRANSACTION_DB_TYPE_INCOME, AccountId: 10, TransactionTime: 100, Amount: 2000}
	if _, err = engine.Insert(account, transaction); err != nil {
		t.Fatal(err)
	}
	session := engine.NewSession()
	defer session.Close()
	deleted, err := reverseStatementCreatedTransaction(session, 1, 20, 1234)
	if err != nil || !deleted {
		t.Fatalf("deleted=%v err=%v", deleted, err)
	}
	var savedTransaction models.Transaction
	if ok, queryErr := engine.ID(20).Get(&savedTransaction); queryErr != nil || !ok || !savedTransaction.Deleted || savedTransaction.DeletedUnixTime != 1234 {
		t.Fatalf("transaction not reversed: ok=%v err=%v row=%+v", ok, queryErr, savedTransaction)
	}
	var savedAccount models.Account
	if ok, queryErr := engine.ID(10).Get(&savedAccount); queryErr != nil || !ok || savedAccount.Balance != 10000 {
		t.Fatalf("account balance=%d ok=%v err=%v, want 10000", savedAccount.Balance, ok, queryErr)
	}
}

func TestParseInstallmentPhase(t *testing.T) {
	current, total, ok := parseInstallmentPhase("消费分期-京东商城 本金 第 11 / 12 期")
	if !ok || current != 11 || total != 12 {
		t.Fatalf("phase=(%d,%d,%v), want (11,12,true)", current, total, ok)
	}
	if _, _, ok = parseInstallmentPhase("分期本金"); ok {
		t.Fatal("description without phase must not be accepted")
	}
	if _, _, ok = parseInstallmentPhase("本金 第13/12期"); ok {
		t.Fatal("current phase greater than total must not be accepted")
	}
}

func TestShortOptionRoundTripLeavesNoPosition(t *testing.T) {
	quantity := float64(0)
	carryingCost := float64(0)
	realized := applyInvestmentPositionTransaction(&quantity, &carryingCost, &hengcai.InvestmentTransaction{
		Action: "SELL", Quantity: 1, QuantityDelta: -1, GrossAmountMinor: 5600, FeesMinor: 213,
	})
	if quantity != -1 || carryingCost != -5387 || realized != 0 {
		t.Fatalf("short open got quantity=%v cost=%v realized=%v", quantity, carryingCost, realized)
	}
	realized = applyInvestmentPositionTransaction(&quantity, &carryingCost, &hengcai.InvestmentTransaction{
		Action: "BUY", Quantity: 1, QuantityDelta: 1, GrossAmountMinor: 1800, FeesMinor: 211,
	})
	if math.Abs(quantity) > 1e-8 || math.Abs(carryingCost) > 1e-8 {
		t.Fatalf("short close left quantity=%v cost=%v", quantity, carryingCost)
	}
	if realized != 3376 {
		t.Fatalf("realized=%v, want 3376", realized)
	}
}

func TestLongPositionRoundTripLeavesNoPosition(t *testing.T) {
	quantity := float64(0)
	carryingCost := float64(0)
	applyInvestmentPositionTransaction(&quantity, &carryingCost, &hengcai.InvestmentTransaction{
		Action: "BUY", Quantity: 2, QuantityDelta: 2, GrossAmountMinor: 20000, FeesMinor: 100,
	})
	realized := applyInvestmentPositionTransaction(&quantity, &carryingCost, &hengcai.InvestmentTransaction{
		Action: "SELL", Quantity: 2, QuantityDelta: -2, GrossAmountMinor: 24000, FeesMinor: 100,
	})
	if math.Abs(quantity) > 1e-8 || math.Abs(carryingCost) > 1e-8 {
		t.Fatalf("long close left quantity=%v cost=%v", quantity, carryingCost)
	}
	if realized != 3800 {
		t.Fatalf("realized=%v, want 3800", realized)
	}
}
