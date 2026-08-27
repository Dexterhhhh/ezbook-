package investmentstatement

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

var chiefTransactionLinePattern = regexp.MustCompile(`^(\d{2}/\d{2}/\d{4})\s+(\d{2}/\d{2}/\d{4})\s+([A-Z0-9]+)\s+(买入|卖出|存入|提取)\s+(.+?)\s+(\(?[\d,]+\.\d{2}\)?)\s+(\(?[\d,]+\.\d{2}\)?)$`)
var chiefSecurityPattern = regexp.MustCompile(`\(#([A-Z0-9.]+)\)`)
var chiefQuantityPattern = regexp.MustCompile(`([\d,.]+)股`)
var chiefPricePattern = regexp.MustCompile(`@([\d,.]+)`)
var chiefNumberPattern = regexp.MustCompile(`\(?[\d,]+(?:\.\d+)?\)?`)
var chiefOptionNamePattern = regexp.MustCompile(`^OPT-([A-Z0-9.]+)\s+(\d{8})\s+(PUT|CALL)\s+([\d.]+)$`)

type chiefRecord struct {
	tradeDate      string
	settlementDate string
	reference      string
	kind           string
	summary        string
	amount         float64
}

type chiefTotals struct {
	statementBuyMinor  int64
	statementSellMinor int64
	parsedBuyMinor     int64
	parsedSellMinor    int64
	openingCashMinor   int64
	closingCashMinor   int64
	interestMinor      int64
}

func parseChief(text string, global bool) (Statement, error) {
	provider := ProviderChiefHK
	market := "HK"
	currency := "HKD"
	if global {
		provider = ProviderChiefGlobal
		market = "US"
		currency = "USD"
	}
	statement := Statement{Provider: provider, Market: market, BaseCurrency: currency, TransactionsValid: true, HoldingsValid: true, CashValid: true}

	account := regexp.MustCompile(`户口编号\s*:\s*([A-Z0-9]+)`).FindStringSubmatch(text)
	statementDate := regexp.MustCompile(`结单日期\s*:\s*(\d{2}/\d{2}/\d{4})`).FindStringSubmatch(text)
	if len(account) < 2 || len(statementDate) < 2 {
		return Statement{}, errorsForChief(global, "缺少账户或结单日期")
	}
	statement.AccountNumber = account[1]
	periodEnd, err := time.Parse("02/01/2006", statementDate[1])
	if err != nil {
		return Statement{}, errorsForChief(global, "结单日期无效")
	}
	statement.PeriodStart = time.Date(periodEnd.Year(), periodEnd.Month(), 1, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
	statement.PeriodEnd = periodEnd.Format("2006-01-02")

	if match := regexp.MustCompile(`投资组合总值 \(港币等值\)\s+(\(?[\d,.]+\)?)`).FindStringSubmatch(text); len(match) == 2 {
		value, _ := parseChiefNumber(match[1])
		statement.PortfolioValueHKDMinor = minor(value)
	}

	totals := chiefTotals{}
	transactionSection := lineSlice(text, "本月资金提存及买卖记录 ("+currency+")", "总买入金额:")
	if opening := regexp.MustCompile(`承上月结馀\s+(\(?[\d,.]+\)?)`).FindStringSubmatch(transactionSection); len(opening) == 2 {
		value, _ := parseChiefNumber(opening[1])
		totals.openingCashMinor = minor(value)
	}
	records := parseChiefRecords(transactionSection)
	for _, record := range records {
		amountMinor := minor(record.amount)
		switch record.kind {
		case "买入", "卖出":
			trade, tradeErr := chiefRecordToTrade(record, currency)
			if tradeErr != nil {
				statement.TransactionsValid = false
				statement.ValidationErrors = append(statement.ValidationErrors, tradeErr.Error())
				continue
			}
			statement.Trades = append(statement.Trades, trade)
			statement.FeesMinor += trade.FeesMinor + trade.TaxesMinor
			if trade.Action == "BUY" {
				totals.parsedBuyMinor += -trade.NetCashAmountMinor
			} else {
				totals.parsedSellMinor += trade.NetCashAmountMinor
			}
		case "存入":
			if amountMinor > 0 {
				statement.DepositsMinor += amountMinor
			}
		case "提取":
			if strings.Contains(record.summary, "利息") {
				totals.interestMinor += absInt64(amountMinor)
				statement.FeesMinor += absInt64(amountMinor)
			} else {
				statement.WithdrawalsMinor += absInt64(amountMinor)
			}
		}
	}

	if match := regexp.MustCompile(`总买入金额:\s*([\d,.]+)\s+总卖出金额:\s*([\d,.]+)`).FindStringSubmatch(text); len(match) == 3 {
		buy, _ := parseNumber(match[1])
		sell, _ := parseNumber(match[2])
		totals.statementBuyMinor = minor(buy)
		totals.statementSellMinor = minor(sell)
	}

	parseChiefCashSummary(text, &statement, &totals)
	statement.Holdings = parseChiefHoldings(text, global, currency)
	if global {
		if match := regexp.MustCompile(`总货值 \(USD\):\s*([\d,.]+)`).FindStringSubmatch(text); len(match) == 2 {
			value, _ := parseNumber(match[1])
			statement.PortfolioValueMinor = minor(value)
		}
	} else if match := regexp.MustCompile(`总货值:\s*([\d,.]+)`).FindStringSubmatch(text); len(match) == 2 {
		value, _ := parseNumber(match[1])
		statement.PortfolioValueMinor = minor(value)
	}
	statement.ClosingNetAssetsMinor = statement.PortfolioValueMinor + totals.closingCashMinor
	statement.CashBalances = append(statement.CashBalances, CashBalance{Currency: currency, Opening: float64(totals.openingCashMinor) / 100, Closing: float64(totals.closingCashMinor) / 100, Deposits: float64(statement.DepositsMinor) / 100, Withdrawals: float64(statement.WithdrawalsMinor) / 100})
	validateChief(&statement, totals)
	return statement, nil
}

func errorsForChief(global bool, detail string) error {
	variant := "香港"
	if global {
		variant = "全球"
	}
	return fmt.Errorf("致富证券%s月结单%s", variant, detail)
}

func parseChiefRecords(section string) []chiefRecord {
	lines := strings.Split(section, "\n")
	result := make([]chiefRecord, 0)
	current := -1
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if match := chiefTransactionLinePattern.FindStringSubmatch(line); len(match) == 8 {
			amount, _ := parseChiefNumber(match[6])
			result = append(result, chiefRecord{tradeDate: match[1], settlementDate: match[2], reference: match[3], kind: match[4], summary: strings.TrimSpace(match[5]), amount: amount})
			current = len(result) - 1
			continue
		}
		if current >= 0 && !strings.HasPrefix(line, "总买入金额:") && !strings.Contains(line, "此单由电脑") {
			result[current].summary += " " + line
		}
	}
	return result
}

func chiefRecordToTrade(record chiefRecord, currency string) (Trade, error) {
	symbolMatch := chiefSecurityPattern.FindStringSubmatch(record.summary)
	quantityMatch := chiefQuantityPattern.FindStringSubmatch(record.summary)
	priceMatch := chiefPricePattern.FindStringSubmatch(record.summary)
	if len(symbolMatch) != 2 || len(quantityMatch) != 2 || len(priceMatch) != 2 {
		return Trade{}, fmt.Errorf("交易 %s 的代码、数量或价格无法识别", record.reference)
	}
	rawQuantity, _ := parseNumber(quantityMatch[1])
	price, _ := parseNumber(priceMatch[1])
	tradeDate, err := mustDate(record.tradeDate, "02/01/2006")
	if err != nil {
		return Trade{}, fmt.Errorf("交易 %s 的日期无效", record.reference)
	}
	settlementDate, _ := mustDate(record.settlementDate, "02/01/2006")
	action := "BUY"
	if record.kind == "卖出" {
		action = "SELL"
	}
	name := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(record.summary, "买入 "), "卖出 "))
	if index := strings.Index(name, "(#"); index >= 0 {
		name = strings.TrimSpace(name[:index])
	}
	assetType := chiefAssetType(name)
	quantity := rawQuantity
	contractMultiplier := 1
	underlyingSymbol := ""
	expirationDate := ""
	optionType := ""
	strikePrice := float64(0)
	if assetType == "OPTION" {
		contractMultiplier = 100
		quantity = rawQuantity / float64(contractMultiplier)
		if math.Abs(quantity-math.Round(quantity)) > 1e-8 {
			return Trade{}, fmt.Errorf("期权交易 %s 的合约单位 %.6f 不能按 100 股乘数换算", record.reference, rawQuantity)
		}
		underlyingSymbol, expirationDate, optionType, strikePrice, err = parseChiefOption(name)
		if err != nil {
			return Trade{}, fmt.Errorf("期权交易 %s: %w", record.reference, err)
		}
	}
	grossMinor := minor(quantity * price * float64(contractMultiplier))
	netMinor := minor(record.amount)
	feeMinor := int64(0)
	if action == "BUY" {
		feeMinor = absInt64(netMinor) - grossMinor
	} else {
		feeMinor = grossMinor - netMinor
	}
	if feeMinor < 0 && feeMinor >= -1 {
		feeMinor = 0
	}
	if feeMinor < 0 {
		return Trade{}, fmt.Errorf("交易 %s 的成交金额与价格数量不相符", record.reference)
	}
	return Trade{Symbol: symbolMatch[1], Name: name, AssetType: assetType, UnderlyingSymbol: underlyingSymbol, ExpirationDate: expirationDate, OptionType: optionType, StrikePrice: strikePrice, ContractMultiplier: contractMultiplier, Currency: currency, TradeDate: tradeDate, SettlementDate: settlementDate, Action: action, Quantity: quantity, Price: price, GrossAmountMinor: grossMinor, FeesMinor: feeMinor, NetCashAmountMinor: netMinor, ExternalReference: record.reference}, nil
}

func parseChiefOption(name string) (string, string, string, float64, error) {
	match := chiefOptionNamePattern.FindStringSubmatch(strings.ToUpper(strings.TrimSpace(name)))
	if len(match) != 5 {
		return "", "", "", 0, fmt.Errorf("期权名称无法拆分: %s", name)
	}
	expiration, err := time.Parse("20060102", match[2])
	if err != nil {
		return "", "", "", 0, fmt.Errorf("到期日无效: %s", match[2])
	}
	strike, err := parseNumber(match[4])
	if err != nil {
		return "", "", "", 0, fmt.Errorf("行权价无效: %s", match[4])
	}
	return match[1], expiration.Format("2006-01-02"), match[3], strike, nil
}

func chiefAssetType(name string) string {
	upper := strings.ToUpper(name)
	if strings.HasPrefix(upper, "OPT-") {
		return "OPTION"
	}
	if strings.Contains(upper, "ETF") || strings.Contains(name, "产品") {
		return "ETF"
	}
	return "STOCK"
}

func parseChiefCashSummary(text string, statement *Statement, totals *chiefTotals) {
	pattern := regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(statement.BaseCurrency) + `\s+(.+)$`)
	match := pattern.FindStringSubmatch(text)
	if len(match) != 2 {
		return
	}
	numbers := chiefNumberPattern.FindAllString(match[1], -1)
	if len(numbers) < 3 {
		return
	}
	exchange, _ := parseChiefNumber(numbers[0])
	statement.ExchangeRateToHKD = exchange
	closing, _ := parseChiefNumber(numbers[len(numbers)-2])
	totals.closingCashMinor = minor(closing)
}

func parseChiefHoldings(text string, global bool, currency string) []Holding {
	section := lineSlice(text, "投资组合 ("+currency+")", "财务协定")
	lines := strings.Split(section, "\n")
	holdings := make([]Holding, 0)
	current := -1
	globalPattern := regexp.MustCompile(`^([A-Z][A-Z0-9.]*)\s+(\(?[\d,.]+\)?)\s+(\(?[\d,.]+\)?)\s+(\(?[\d,.]+\)?)\s+(\(?[\d,.]+\)?)\s+([\d,.]+)\s+([\d,.]+)(?:\s+(.*?))?\s+(\d{2}/\d{2}/\d{4})$`)
	hkPattern := regexp.MustCompile(`^(\d{5})\s+(.+?)\s+(\(?[\d,.]+\)?)\s+(\(?[\d,.]+\)?)\s+(\(?[\d,.]+\)?)\s+([\d,.]+)\s+([\d,.]+)(?:\s+(.*?))?\s+(\d{2}/\d{2}/\d{4})$`)
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.Contains(line, "总货值") {
			continue
		}
		if global {
			if match := globalPattern.FindStringSubmatch(line); len(match) == 10 {
				opening, _ := parseChiefNumber(match[2])
				closing, _ := parseChiefNumber(match[4])
				price, _ := parseNumber(match[6])
				marketValue, _ := parseNumber(match[7])
				averageCost := chiefAverageCost(match[8], closing)
				holdings = append(holdings, Holding{Symbol: match[1], AssetType: "STOCK", Currency: currency, OpeningQuantity: opening, ClosingQuantity: closing, AverageCost: averageCost, CostBasisMinor: minor(closing * averageCost), ClosingPrice: price, MarketValueMinor: minor(marketValue), UnrealizedPnlMinor: minor(marketValue - closing*averageCost)})
				current = len(holdings) - 1
				continue
			}
		} else if match := hkPattern.FindStringSubmatch(line); len(match) == 10 {
			opening, _ := parseChiefNumber(match[3])
			closing, _ := parseChiefNumber(match[5])
			price, _ := parseNumber(match[6])
			marketValue, _ := parseNumber(match[7])
			averageCost := chiefAverageCost(match[8], closing)
			holdings = append(holdings, Holding{Symbol: match[1], Name: strings.TrimSpace(match[2]), AssetType: "ETF", Currency: currency, OpeningQuantity: opening, ClosingQuantity: closing, AverageCost: averageCost, CostBasisMinor: minor(closing * averageCost), ClosingPrice: price, MarketValueMinor: minor(marketValue), UnrealizedPnlMinor: minor(marketValue - closing*averageCost)})
			current = len(holdings) - 1
			continue
		}
		if current >= 0 && !strings.HasPrefix(line, "股票") && !strings.HasPrefix(line, "证券代号") && !strings.HasPrefix(line, "市场") && !strings.HasPrefix(line, "率%") && !strings.HasPrefix(line, "价") {
			holdings[current].Name = strings.TrimSpace(holdings[current].Name + " " + line)
		}
	}
	for index := range holdings {
		holdings[index].Name = strings.TrimSpace(holdings[index].Name)
		if holdings[index].Name == "" {
			holdings[index].Name = holdings[index].Symbol
		}
		holdings[index].AssetType = chiefAssetType(holdings[index].Name)
	}
	return holdings
}

func chiefAverageCost(raw string, closingQuantity float64) float64 {
	if closingQuantity <= 0 {
		return 0
	}
	numbers := chiefNumberPattern.FindAllString(raw, -1)
	if len(numbers) == 0 {
		return 0
	}
	value, _ := parseChiefNumber(numbers[len(numbers)-1])
	return value
}

func validateChief(statement *Statement, totals chiefTotals) {
	label := "致富证券香港"
	if statement.Provider == ProviderChiefGlobal {
		label = "致富证券全球"
	}
	if len(statement.Trades) == 0 {
		statement.TransactionsValid = false
		statement.ValidationErrors = append(statement.ValidationErrors, label+"月结单没有识别到证券交易")
	}
	if totals.parsedBuyMinor != totals.statementBuyMinor || totals.parsedSellMinor != totals.statementSellMinor {
		statement.TransactionsValid = false
		statement.ValidationErrors = append(statement.ValidationErrors, fmt.Sprintf("%s交易金额合计与月结单买卖总额不相符", label))
	}

	deltas := make(map[string]float64)
	for _, trade := range statement.Trades {
		delta := trade.Quantity
		if trade.Action == "SELL" {
			delta = -delta
		}
		deltas[trade.Symbol] += delta
	}
	marketValueMinor := int64(0)
	for _, holding := range statement.Holdings {
		if math.Abs(holding.OpeningQuantity+deltas[holding.Symbol]-holding.ClosingQuantity) > 1e-8 {
			statement.HoldingsValid = false
			statement.ValidationErrors = append(statement.ValidationErrors, fmt.Sprintf("%s 的期初持仓、交易和期末持仓不相符", holding.Symbol))
		}
		delete(deltas, holding.Symbol)
		marketValueMinor += holding.MarketValueMinor
	}
	for symbol, delta := range deltas {
		if math.Abs(delta) > 1e-8 {
			statement.HoldingsValid = false
			statement.ValidationErrors = append(statement.ValidationErrors, fmt.Sprintf("%s 的交易净数量没有对应期末持仓", symbol))
		}
	}
	if len(statement.Holdings) == 0 || marketValueMinor != statement.PortfolioValueMinor {
		statement.HoldingsValid = false
		statement.ValidationErrors = append(statement.ValidationErrors, label+"持仓市值合计与投资组合总值不相符")
	}

	netTradeCashMinor := totals.parsedSellMinor - totals.parsedBuyMinor
	expectedClosingCash := totals.openingCashMinor + statement.DepositsMinor - statement.WithdrawalsMinor + netTradeCashMinor - totals.interestMinor
	if expectedClosingCash != totals.closingCashMinor {
		statement.CashValid = false
		statement.ValidationErrors = append(statement.ValidationErrors, label+"期初现金、提存、买卖和期末现金不相符")
	}
	if statement.ClosingNetAssetsMinor != statement.PortfolioValueMinor+totals.closingCashMinor {
		statement.CashValid = false
		statement.ValidationErrors = append(statement.ValidationErrors, label+"期末现金及持仓与资产净值不相符")
	}
	if statement.ExchangeRateToHKD > 0 && statement.PortfolioValueHKDMinor > 0 {
		expectedHKD := int64(math.Round(float64(statement.PortfolioValueMinor) * statement.ExchangeRateToHKD))
		if absInt64(expectedHKD-statement.PortfolioValueHKDMinor) > 2 {
			statement.CashValid = false
			statement.ValidationErrors = append(statement.ValidationErrors, label+"投资组合港币等值与汇率不相符")
		}
	}
	statement.Ready = statement.TransactionsValid && statement.HoldingsValid && statement.CashValid
}

func parseChiefNumber(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	negative := strings.HasPrefix(raw, "(") && strings.HasSuffix(raw, ")")
	raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(raw, "("), ")"))
	value, err := parseNumber(raw)
	if negative {
		value = -value
	}
	return value, err
}
