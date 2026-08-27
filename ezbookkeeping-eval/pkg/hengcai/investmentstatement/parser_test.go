package investmentstatement

import (
	"math"
	"os"
	"testing"
)

func parseFixturePDF(t *testing.T, envName string) Statement {
	return parseFixturePDFWithEngine(t, envName, "", EngineAuto)
}

func parseFixturePDFWithEngine(t *testing.T, envName, passwordEnv, engine string) Statement {
	t.Helper()
	path := os.Getenv(envName)
	if path == "" {
		t.Skipf("%s is not set", envName)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	password := ""
	if passwordEnv != "" {
		password = os.Getenv(passwordEnv)
		if password == "" {
			t.Skipf("%s is not set", passwordEnv)
		}
	}
	statement, err := ParsePDFWithEngine(file, info.Size(), password, engine)
	if err != nil {
		t.Fatal(err)
	}
	return statement
}

func holdingBySymbol(statement Statement, symbol string) (Holding, bool) {
	for _, holding := range statement.Holdings {
		if holding.Symbol == symbol {
			return holding, true
		}
	}
	return Holding{}, false
}

func tradeBySymbol(statement Statement, symbol string) (Trade, bool) {
	for _, trade := range statement.Trades {
		if trade.Symbol == symbol {
			return trade, true
		}
	}
	return Trade{}, false
}

func TestRealHSBCStatement(t *testing.T) {
	statement := parseFixturePDF(t, "HENGCAI_HSBC_STATEMENT")
	if statement.Provider != ProviderHSBC || statement.PeriodStart != "2026-07-11" || statement.PeriodEnd != "2026-08-10" {
		t.Fatalf("unexpected header: %#v", statement)
	}
	if len(statement.Trades) != 2 || len(statement.Holdings) != 5 {
		t.Fatalf("trades=%d holdings=%d errors=%v", len(statement.Trades), len(statement.Holdings), statement.ValidationErrors)
	}
	if statement.PortfolioValueMinor != 316121 || statement.PortfolioValueHKDMinor != 2480111 || statement.FeesMinor != 1 {
		t.Fatalf("unexpected totals: %#v", statement)
	}
	if !statement.Ready {
		t.Fatalf("statement did not reconcile: %v", statement.ValidationErrors)
	}
}

func TestRealIBKRStatement(t *testing.T) {
	statement := parseFixturePDF(t, "HENGCAI_IBKR_STATEMENT")
	if statement.Provider != ProviderIBKR || statement.PeriodStart != "2026-07-01" || statement.PeriodEnd != "2026-07-31" {
		t.Fatalf("unexpected header: %#v", statement)
	}
	if len(statement.Trades) != 5 || len(statement.Holdings) != 3 {
		t.Fatalf("trades=%d holdings=%d errors=%v", len(statement.Trades), len(statement.Holdings), statement.ValidationErrors)
	}
	if statement.OpeningNetAssetsMinor != 6176 || statement.ClosingNetAssetsMinor != 193708 || statement.DepositsMinor != 227530 || statement.FeesMinor != 242 {
		t.Fatalf("unexpected cash totals: %#v", statement)
	}
	if statement.RealizedPnlMinor != -2075 || statement.UnrealizedPnlMinor != -37858 || statement.TotalPnlMinor != -39934 || statement.AccountTotalPnlMinor != -39998 || statement.PortfolioValueMinor != 177561 {
		t.Fatalf("unexpected performance totals: %#v", statement)
	}
	if !statement.Ready {
		t.Fatalf("statement did not reconcile: %v", statement.ValidationErrors)
	}
}

func TestRealChiefHKStatement(t *testing.T) {
	statement := parseFixturePDFWithEngine(t, "HENGCAI_CHIEF_HK_STATEMENT", "HENGCAI_CHIEF_PDF_PASSWORD", EngineChiefHK)
	if statement.Provider != ProviderChiefHK || statement.Market != "HK" || statement.BaseCurrency != "HKD" || statement.PeriodStart != "2026-07-01" || statement.PeriodEnd != "2026-07-31" {
		t.Fatalf("unexpected header: %#v", statement)
	}
	if len(statement.Trades) != 18 || len(statement.Holdings) != 2 {
		t.Fatalf("trades=%d holdings=%d errors=%v", len(statement.Trades), len(statement.Holdings), statement.ValidationErrors)
	}
	if statement.PortfolioValueMinor != 774600 || statement.ClosingNetAssetsMinor != 425540 || statement.DepositsMinor != 50000 || statement.WithdrawalsMinor != 1000000 || statement.FeesMinor != 32010 {
		t.Fatalf("unexpected totals: %#v", statement)
	}
	holding, ok := holdingBySymbol(statement, "07747")
	if !ok || holding.OpeningQuantity != 0 || holding.ClosingQuantity != 100 || holding.ClosingPrice != 77.46 || holding.MarketValueMinor != 774600 {
		t.Fatalf("unexpected 07747 holding: %#v", holding)
	}
	if !statement.Ready {
		t.Fatalf("statement did not reconcile: %v", statement.ValidationErrors)
	}
}

func TestRealChiefGlobalStatement(t *testing.T) {
	statement := parseFixturePDFWithEngine(t, "HENGCAI_CHIEF_GLOBAL_STATEMENT", "HENGCAI_CHIEF_PDF_PASSWORD", EngineChiefGlobal)
	if statement.Provider != ProviderChiefGlobal || statement.Market != "US" || statement.BaseCurrency != "USD" || statement.PeriodStart != "2026-07-01" || statement.PeriodEnd != "2026-07-31" {
		t.Fatalf("unexpected header: %#v", statement)
	}
	if len(statement.Trades) != 85 || len(statement.Holdings) != 8 {
		t.Fatalf("trades=%d holdings=%d errors=%v", len(statement.Trades), len(statement.Holdings), statement.ValidationErrors)
	}
	if statement.PortfolioValueMinor != 155658 || statement.PortfolioValueHKDMinor != 1220592 || statement.ClosingNetAssetsMinor != 142852 || statement.WithdrawalsMinor != 100000 || statement.FeesMinor != 9883 {
		t.Fatalf("unexpected totals: %#v", statement)
	}
	for symbol, quantity := range map[string]float64{"AAPL": 1, "GOOG": 2, "MU": 0.3, "SKHY": 2} {
		holding, ok := holdingBySymbol(statement, symbol)
		if !ok || math.Abs(holding.ClosingQuantity-quantity) > 1e-8 {
			t.Fatalf("unexpected %s holding: %#v", symbol, holding)
		}
	}
	option, ok := tradeBySymbol(statement, "DRAM6S10005000")
	if !ok || option.AssetType != "OPTION" || option.UnderlyingSymbol != "DRAM" || option.ExpirationDate != "2026-07-10" || option.OptionType != "PUT" || option.StrikePrice != 50 || option.ContractMultiplier != 100 || option.Quantity != 1 || option.ExternalReference != "S910490099" {
		t.Fatalf("unexpected option trade: %#v", option)
	}
	if !statement.Ready {
		t.Fatalf("statement did not reconcile: %v", statement.ValidationErrors)
	}
}
