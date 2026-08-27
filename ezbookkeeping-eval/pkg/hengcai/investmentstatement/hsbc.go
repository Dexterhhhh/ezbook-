package investmentstatement

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

var hsbcHoldingPattern = regexp.MustCompile(`(?m)^\s*([A-Z][A-Z0-9.]+)\s+(.+?)\n\s*Risk Lvl\s+\S+\s+([\d,.]+)\s+([\d,.]+)\s+([A-Z]{3})\s+([\d,.]+)\s+[A-Z]{3}\s+([\d,.]+)\s*$`)
var hsbcTradePattern = regexp.MustCompile(`(?m)^\s*([A-Z][A-Z0-9.]+)\s+(.+?)\n\s*(\d{2}[A-Z]{3}\d{4})\s+(\d{2}[A-Z]{3}\d{4})\s+([A-Z]{3})\s+([\d,.]+)\s+([\d,.]+-?)\s+[A-Z]{3}\s+([\d,.]+)\n\s*Reference:\s*(\S+)\s+Type:\s*(PUR|SAL)\s*$`)

func parseHSBC(text string) (Statement, error) {
	statement := Statement{Provider: ProviderHSBC, Market: "US", BaseCurrency: "USD", TransactionsValid: true, HoldingsValid: true, CashValid: true}
	account := regexp.MustCompile(`A/C no\s*:\s*([\d-]+)`).FindStringSubmatch(text)
	period := regexp.MustCompile(`Period\s*:\s*From\s+(\d{2}[A-Z]{3}\d{4})\s+to\s+(\d{2}[A-Z]{3}\d{4})`).FindStringSubmatch(text)
	if len(account) < 2 || len(period) < 3 {
		return Statement{}, fmt.Errorf("汇丰对账单缺少账户或结单周期")
	}
	statement.AccountNumber = account[1]
	var err error
	statement.PeriodStart, err = mustDate(period[1], "02Jan2006")
	if err != nil {
		return Statement{}, fmt.Errorf("汇丰结单开始日期无效: %w", err)
	}
	statement.PeriodEnd, err = mustDate(period[2], "02Jan2006")
	if err != nil {
		return Statement{}, fmt.Errorf("汇丰结单结束日期无效: %w", err)
	}
	if match := regexp.MustCompile(`Portfolio summary \(HKD equivalent\)[\s\S]*?FOREIGN SHARES\s+([\d,.]+)`).FindStringSubmatch(text); len(match) == 2 {
		value, _ := parseNumber(match[1])
		statement.PortfolioValueHKDMinor = minor(value)
	}
	if match := regexp.MustCompile(`Total portfolio value\s+USD\s+([\d,.]+)`).FindStringSubmatch(text); len(match) == 2 {
		value, _ := parseNumber(match[1])
		statement.PortfolioValueMinor = minor(value)
		statement.ClosingNetAssetsMinor = statement.PortfolioValueMinor
	}
	if match := regexp.MustCompile(`Exchange rate against HKD\s*:\s*USD\s+([\d.]+)`).FindStringSubmatch(text); len(match) == 2 {
		statement.ExchangeRateToHKD, _ = parseNumber(match[1])
	}

	holdingsSection := lineSlice(text, "Portfolio details", "Transaction summary")
	for _, match := range hsbcHoldingPattern.FindAllStringSubmatch(holdingsSection, -1) {
		opening, _ := parseNumber(match[3])
		closing, _ := parseNumber(match[4])
		price, _ := parseNumber(match[6])
		marketValue, _ := parseNumber(match[7])
		statement.Holdings = append(statement.Holdings, Holding{Symbol: match[1], Name: strings.TrimSpace(match[2]), AssetType: "STOCK", Currency: match[5], OpeningQuantity: opening, ClosingQuantity: closing, ClosingPrice: price, MarketValueMinor: minor(marketValue)})
	}

	tradesSection := lineSlice(text, "Transaction summary", "Thank you for choosing HSBC")
	for _, match := range hsbcTradePattern.FindAllStringSubmatch(tradesSection, -1) {
		tradeDate, dateErr := mustDate(match[3], "02Jan2006")
		if dateErr != nil {
			return Statement{}, dateErr
		}
		settlementDate, _ := mustDate(match[4], "02Jan2006")
		price, _ := parseNumber(match[6])
		quantity, _ := parseNumber(match[7])
		settlement, _ := parseNumber(match[8])
		action := "BUY"
		if match[10] == "SAL" || quantity < 0 {
			action = "SELL"
		}
		quantity = math.Abs(quantity)
		gross := minor(quantity * price)
		trade := Trade{Symbol: match[1], Name: strings.TrimSpace(match[2]), AssetType: "STOCK", Currency: match[5], TradeDate: tradeDate, SettlementDate: settlementDate, Action: action, Quantity: quantity, Price: price, GrossAmountMinor: gross, ExternalReference: match[9]}
		if action == "BUY" {
			trade.NetCashAmountMinor = -minor(settlement)
		} else {
			trade.NetCashAmountMinor = minor(settlement)
			trade.FeesMinor = gross - trade.NetCashAmountMinor
		}
		statement.Trades = append(statement.Trades, trade)
	}
	if match := regexp.MustCompile(`Total charges and income\s+USD\s+([\d,.]+)\s+USD`).FindStringSubmatch(text); len(match) == 2 {
		value, _ := parseNumber(match[1])
		statement.FeesMinor = minor(value)
	}
	validateHSBC(&statement)
	return statement, nil
}

func validateHSBC(statement *Statement) {
	if len(statement.Trades) == 0 {
		statement.TransactionsValid = false
		statement.ValidationErrors = append(statement.ValidationErrors, "没有识别到汇丰证券交易")
	}
	if len(statement.Holdings) == 0 {
		statement.HoldingsValid = false
		statement.ValidationErrors = append(statement.ValidationErrors, "没有识别到汇丰证券持仓")
	}
	deltas := make(map[string]float64)
	fees := int64(0)
	for _, trade := range statement.Trades {
		delta := trade.Quantity
		if trade.Action == "SELL" {
			delta = -delta
		}
		deltas[trade.Symbol] += delta
		fees += trade.FeesMinor + trade.TaxesMinor
	}
	marketValue := int64(0)
	for _, holding := range statement.Holdings {
		if math.Abs(holding.OpeningQuantity+deltas[holding.Symbol]-holding.ClosingQuantity) > 1e-8 {
			statement.HoldingsValid = false
			statement.ValidationErrors = append(statement.ValidationErrors, fmt.Sprintf("%s 的期初持仓、交易和期末持仓不相符", holding.Symbol))
		}
		marketValue += holding.MarketValueMinor
	}
	if marketValue != statement.PortfolioValueMinor {
		statement.HoldingsValid = false
		statement.ValidationErrors = append(statement.ValidationErrors, fmt.Sprintf("持仓市值合计 %d 与组合总值 %d 不相符", marketValue, statement.PortfolioValueMinor))
	}
	if fees != statement.FeesMinor {
		statement.TransactionsValid = false
		statement.ValidationErrors = append(statement.ValidationErrors, fmt.Sprintf("交易费用合计 %d 与结单费用 %d 不相符", fees, statement.FeesMinor))
	}
	if statement.ExchangeRateToHKD > 0 {
		expectedHKD := int64(math.Round(float64(statement.PortfolioValueMinor) * statement.ExchangeRateToHKD))
		if absInt64(expectedHKD-statement.PortfolioValueHKDMinor) > 1 {
			statement.CashValid = false
			statement.ValidationErrors = append(statement.ValidationErrors, "港币等值组合总额与美元市值及汇率不相符")
		}
	}
	statement.Ready = statement.TransactionsValid && statement.HoldingsValid && statement.CashValid
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}
