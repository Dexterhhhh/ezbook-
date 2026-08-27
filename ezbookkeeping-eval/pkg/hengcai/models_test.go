package hengcai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInvestmentModelsUseFrontendJSONFieldNames(t *testing.T) {
	account := InvestmentAccount{Id: 7, AccountType: "BROKERAGE", BaseCurrency: "USD", AccountId: 3837414098547507200}
	encoded, err := json.Marshal(account)
	if err != nil {
		t.Fatalf("marshal investment account: %v", err)
	}
	payload := string(encoded)
	for _, expected := range []string{`"id":7`, `"account_type":"BROKERAGE"`, `"base_currency":"USD"`, `"account_id":"3837414098547507200"`} {
		if !strings.Contains(payload, expected) {
			t.Fatalf("expected %s in %s", expected, payload)
		}
	}
	if strings.Contains(payload, "AccountType") {
		t.Fatalf("unexpected Go field name in %s", payload)
	}
}

func TestInvestmentTransactionAcceptsFrontendJSONFieldNames(t *testing.T) {
	var transaction InvestmentTransaction
	err := json.Unmarshal([]byte(`{"investment_account_id":2,"instrument_id":3,"traded_at":123,"action":"BUY","quantity":1.5,"price":12.34,"fees_minor":10}`), &transaction)
	if err != nil {
		t.Fatalf("unmarshal investment transaction: %v", err)
	}
	if transaction.InvestmentAccountId != 2 || transaction.InstrumentId != 3 || transaction.TradedAt != 123 || transaction.FeesMinor != 10 {
		t.Fatalf("unexpected transaction: %+v", transaction)
	}
}

func TestAlpacaSettingAcceptsFrontendJSONFieldNames(t *testing.T) {
	var setting AlpacaSetting
	err := json.Unmarshal([]byte(`{"environment":"PAPER","api_key_id":"key-id","secret_key":"secret","trading_url":"https://paper-api.alpaca.markets/v2","data_url":"https://data.alpaca.markets"}`), &setting)
	if err != nil {
		t.Fatalf("unmarshal alpaca setting: %v", err)
	}
	if setting.ApiKeyId != "key-id" || setting.SecretKey != "secret" || setting.TradingUrl == "" || setting.DataUrl == "" {
		t.Fatalf("unexpected alpaca setting: %+v", setting)
	}
}
