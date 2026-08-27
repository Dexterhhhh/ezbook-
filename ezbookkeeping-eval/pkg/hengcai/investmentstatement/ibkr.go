package investmentstatement

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

var ibkrTradeRowPattern = regexp.MustCompile(`(?m)^([A-Z][A-Z0-9.]*)\s+(-?[\d,.]+)\s+([\d,.]+)\s+([\d,.]+)\s+(-?[\d,.]+)\s+(-?[\d,.]+)\s+(-?[\d,.]+)\s+(-?[\d,.]+)\s+(-?[\d,.]+)\s+([OC])\s*$`)
var ibkrHoldingRowPattern = regexp.MustCompile(`(?m)^([A-Z][A-Z0-9.]*)\s+([\d,.]+)\s+([\d,.]+)\s+([\d,.]+)\s+([\d,.]+)\s+([\d,.]+)\s+([\d,.]+)\s+(-?[\d,.]+)\s*$`)

func parseIBKR(text string) (Statement, error) {
	statement := Statement{Provider: ProviderIBKR, Market: "US", BaseCurrency: "USD", TransactionsValid: true, HoldingsValid: true, CashValid: true}
	account := regexp.MustCompile(`(?m)^账户\s+([A-Z]\d+)\s*$`).FindStringSubmatch(text)
	period := regexp.MustCompile(`(?m)^([一二三四五六七八九十]+)月\s+(\d{1,2}),\s+(\d{4})\s+-\s+([一二三四五六七八九十]+)月\s+(\d{1,2}),\s+(\d{4})\s*$`).FindStringSubmatch(text)
	if len(account) < 2 || len(period) < 7 {
		return Statement{}, fmt.Errorf("IBKR 对账单缺少账户或活动周期")
	}
	statement.AccountNumber = account[1]
	startMonth, okStart := chineseMonth(period[1])
	endMonth, okEnd := chineseMonth(period[4])
	if !okStart || !okEnd {
		return Statement{}, fmt.Errorf("IBKR 活动周期月份无法识别")
	}
	start, _ := time.Parse("2006-1-2", fmt.Sprintf("%s-%d-%s", period[3], startMonth, period[2]))
	end, _ := time.Parse("2006-1-2", fmt.Sprintf("%s-%d-%s", period[6], endMonth, period[5]))
	statement.PeriodStart = start.Format("2006-01-02")
	statement.PeriodEnd = end.Format("2006-01-02")
	if match := regexp.MustCompile(`开始价值\s+([\d,.]+)`).FindStringSubmatch(text); len(match) == 2 {
		value, _ := parseNumber(match[1])
		statement.OpeningNetAssetsMinor = minor(value)
	}
	if match := regexp.MustCompile(`结束价值\s+([\d,.]+)`).FindStringSubmatch(text); len(match) == 2 {
		value, _ := parseNumber(match[1])
		statement.ClosingNetAssetsMinor = minor(value)
	}
	if match := regexp.MustCompile(`存款和取款\s+([\d,.]+)`).FindStringSubmatch(text); len(match) == 2 {
		value, _ := parseNumber(match[1])
		statement.DepositsMinor = minor(value)
	}
	if match := regexp.MustCompile(`佣金\s+(-?[\d,.]+)`).FindStringSubmatch(text); len(match) == 2 {
		value, _ := parseNumber(match[1])
		statement.FeesMinor = int64(math.Abs(float64(minor(value))))
	}
	if match := regexp.MustCompile(`时间加权的收益率\s+(-?[\d.]+)%`).FindStringSubmatch(text); len(match) == 2 {
		value, _ := parseNumber(match[1])
		statement.TimeWeightedReturnBps = int(math.Round(value * 100))
	}
	if match := regexp.MustCompile(`(?m)^总数 股票\s+0\.00\s+0\.00\s+(-?[\d,.]+)\s+0\.00\s+0\.00\s+(-?[\d,.]+)\s+0\.00\s+(-?[\d,.]+)\s+0\.00\s+0\.00\s+(-?[\d,.]+)\s+(-?[\d,.]+)\s*$`).FindStringSubmatch(lineSlice(text, "已实现和未实现的表现总结", "现金报告")); len(match) == 6 {
		realized, _ := parseNumber(match[2])
		unrealized, _ := parseNumber(match[4])
		total, _ := parseNumber(match[5])
		statement.RealizedPnlMinor = minor(realized)
		statement.UnrealizedPnlMinor = minor(unrealized)
		statement.TotalPnlMinor = minor(total)
	}
	if match := regexp.MustCompile(`(?m)^总计（全部资产）\s+0\.00\s+0\.00\s+(-?[\d,.]+)\s+0\.00\s+0\.00\s+(-?[\d,.]+)\s+0\.00\s+(-?[\d,.]+)\s+0\.00\s+0\.00\s+(-?[\d,.]+)\s+(-?[\d,.]+)\s*$`).FindStringSubmatch(lineSlice(text, "已实现和未实现的表现总结", "现金报告")); len(match) == 6 {
		total, _ := parseNumber(match[5])
		statement.AccountTotalPnlMinor = minor(total)
	}

	productNames := parseIBKRProducts(text)
	holdingsSection := lineSlice(text, "未平仓持仓", "外汇结余")
	for _, match := range ibkrHoldingRowPattern.FindAllStringSubmatch(holdingsSection, -1) {
		quantity, _ := parseNumber(match[2])
		averageCost, _ := parseNumber(match[4])
		costBasis, _ := parseNumber(match[5])
		closePrice, _ := parseNumber(match[6])
		marketValue, _ := parseNumber(match[7])
		unrealized, _ := parseNumber(match[8])
		name := productNames[match[1]].name
		assetType := productNames[match[1]].assetType
		if assetType == "" {
			assetType = "STOCK"
		}
		statement.Holdings = append(statement.Holdings, Holding{Symbol: match[1], Name: name, AssetType: assetType, Currency: "USD", ClosingQuantity: quantity, AverageCost: averageCost, CostBasisMinor: minor(costBasis), ClosingPrice: closePrice, MarketValueMinor: minor(marketValue), UnrealizedPnlMinor: minor(unrealized)})
	}

	tradeSection := lineSlice(text, "交易\n按市值计算的", "=== PAGE 4 ===")
	for _, indexes := range ibkrTradeRowPattern.FindAllStringSubmatchIndex(tradeSection, -1) {
		match := ibkrTradeRowPattern.FindStringSubmatch(tradeSection[indexes[0]:indexes[1]])
		prefixStart := indexes[0] - 80
		if prefixStart < 0 {
			prefixStart = 0
		}
		prefix := tradeSection[prefixStart:indexes[0]]
		dateMatch := regexp.MustCompile(`(\d{4}-\d{2}-\d{2}),\s*$`).FindStringSubmatch(prefix)
		if len(dateMatch) != 2 {
			continue
		}
		quantity, _ := parseNumber(match[2])
		price, _ := parseNumber(match[3])
		grossSigned, _ := parseNumber(match[5])
		feeSigned, _ := parseNumber(match[6])
		realized, _ := parseNumber(match[8])
		action := "BUY"
		if quantity < 0 {
			action = "SELL"
		}
		quantity = math.Abs(quantity)
		gross := int64(math.Abs(float64(minor(grossSigned))))
		fee := int64(math.Abs(float64(minor(feeSigned))))
		name := productNames[match[1]].name
		assetType := productNames[match[1]].assetType
		if assetType == "" {
			assetType = "STOCK"
		}
		trade := Trade{Symbol: match[1], Name: name, AssetType: assetType, Currency: "USD", TradeDate: dateMatch[1], Action: action, Quantity: quantity, Price: price, GrossAmountMinor: gross, FeesMinor: fee, RealizedPnlMinor: minor(realized), ExternalReference: fmt.Sprintf("%s:%s:%s", match[1], dateMatch[1], match[10])}
		if action == "BUY" {
			trade.NetCashAmountMinor = -(gross + fee)
		} else {
			trade.NetCashAmountMinor = gross - fee
		}
		statement.Trades = append(statement.Trades, trade)
	}
	parseIBKRCash(text, &statement)
	validateIBKR(&statement)
	return statement, nil
}

type ibkrProduct struct{ name, assetType string }

func parseIBKRProducts(text string) map[string]ibkrProduct {
	result := make(map[string]ibkrProduct)
	section := lineSlice(text, "金融产品信息", "代码\n代码 意思")
	pattern := regexp.MustCompile(`(?m)^([A-Z][A-Z0-9.]*)\s+(.+?)\s+\d+\s+[A-Z0-9]+\s+[A-Z0-9.]+\s+[A-Z]+\s+1\s+(ETF|COMMON|ADR)\s*$`)
	for _, match := range pattern.FindAllStringSubmatch(section, -1) {
		assetType := "STOCK"
		if match[3] == "ETF" {
			assetType = "ETF"
		}
		result[match[1]] = ibkrProduct{name: strings.TrimSpace(match[2]), assetType: assetType}
	}
	return result
}

func parseIBKRCash(text string, statement *Statement) {
	section := lineSlice(text, "现金报告", "未平仓持仓")
	for _, currency := range []string{"HKD", "USD"} {
		currencySection := lineSlice(section, "\n"+currency+"\n现金细节", "\n"+map[string]string{"HKD": "USD", "USD": "活动账单"}[currency])
		if currencySection == "" {
			continue
		}
		balance := CashBalance{Currency: currency}
		if match := regexp.MustCompile(`期初现金\s+([\d,.]+)`).FindStringSubmatch(currencySection); len(match) == 2 {
			balance.Opening, _ = parseNumber(match[1])
		}
		if match := regexp.MustCompile(`期末现金\s+([\d,.]+)`).FindStringSubmatch(currencySection); len(match) == 2 {
			balance.Closing, _ = parseNumber(match[1])
		}
		if match := regexp.MustCompile(`存款\s+([\d,.]+)`).FindStringSubmatch(currencySection); len(match) == 2 {
			balance.Deposits, _ = parseNumber(match[1])
		}
		statement.CashBalances = append(statement.CashBalances, balance)
	}
}

func validateIBKR(statement *Statement) {
	if len(statement.Trades) == 0 {
		statement.TransactionsValid = false
		statement.ValidationErrors = append(statement.ValidationErrors, "没有识别到 IBKR 股票交易")
	}
	if len(statement.Holdings) == 0 {
		statement.HoldingsValid = false
		statement.ValidationErrors = append(statement.ValidationErrors, "没有识别到 IBKR 期末持仓")
	}
	fees := int64(0)
	quantities := make(map[string]float64)
	for _, trade := range statement.Trades {
		fees += trade.FeesMinor + trade.TaxesMinor
		delta := trade.Quantity
		if trade.Action == "SELL" {
			delta = -delta
		}
		quantities[trade.Symbol] += delta
	}
	if absInt64(fees-statement.FeesMinor) > 1 {
		statement.TransactionsValid = false
		statement.ValidationErrors = append(statement.ValidationErrors, fmt.Sprintf("IBKR 交易佣金合计 %d 与账单佣金 %d 不相符", fees, statement.FeesMinor))
	}
	marketValue := int64(0)
	unrealized := int64(0)
	for _, holding := range statement.Holdings {
		if math.Abs(quantities[holding.Symbol]-holding.ClosingQuantity) > 1e-8 {
			statement.HoldingsValid = false
			statement.ValidationErrors = append(statement.ValidationErrors, fmt.Sprintf("%s 的交易净数量与期末持仓不相符", holding.Symbol))
		}
		marketValue += holding.MarketValueMinor
		unrealized += holding.UnrealizedPnlMinor
	}
	statement.PortfolioValueMinor = marketValue
	if statement.UnrealizedPnlMinor != 0 && unrealized != statement.UnrealizedPnlMinor {
		statement.HoldingsValid = false
		statement.ValidationErrors = append(statement.ValidationErrors, "IBKR 持仓未实现损益与表现汇总不相符")
	}
	if absInt64(statement.OpeningNetAssetsMinor+statement.DepositsMinor+statement.AccountTotalPnlMinor-statement.ClosingNetAssetsMinor) > 2 {
		statement.CashValid = false
		statement.ValidationErrors = append(statement.ValidationErrors, "IBKR 期初净值、存款、损益与期末净值不相符")
	}
	statement.Ready = statement.TransactionsValid && statement.HoldingsValid && statement.CashValid
}

func chineseMonth(value string) (int, bool) {
	months := map[string]int{"一": 1, "二": 2, "三": 3, "四": 4, "五": 5, "六": 6, "七": 7, "八": 8, "九": 9, "十": 10, "十一": 11, "十二": 12}
	month, ok := months[value]
	return month, ok
}
