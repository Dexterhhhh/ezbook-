package investmentstatement

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	pdf "github.com/ledongthuc/pdf"
	pdfcpuapi "github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

const (
	ProviderHSBC        = "HSBC"
	ProviderIBKR        = "IBKR"
	ProviderChiefHK     = "CHIEF_HK"
	ProviderChiefGlobal = "CHIEF_GLOBAL"

	EngineAuto        = "AUTO"
	EngineHSBC        = ProviderHSBC
	EngineIBKR        = ProviderIBKR
	EngineChiefHK     = ProviderChiefHK
	EngineChiefGlobal = ProviderChiefGlobal
)

type Statement struct {
	Provider               string        `json:"provider"`
	Market                 string        `json:"market"`
	AccountNumber          string        `json:"account_number"`
	PeriodStart            string        `json:"period_start"`
	PeriodEnd              string        `json:"period_end"`
	BaseCurrency           string        `json:"base_currency"`
	OpeningNetAssetsMinor  int64         `json:"opening_net_assets_minor"`
	ClosingNetAssetsMinor  int64         `json:"closing_net_assets_minor"`
	DepositsMinor          int64         `json:"deposits_minor"`
	WithdrawalsMinor       int64         `json:"withdrawals_minor"`
	FeesMinor              int64         `json:"fees_minor"`
	RealizedPnlMinor       int64         `json:"realized_pnl_minor"`
	UnrealizedPnlMinor     int64         `json:"unrealized_pnl_minor"`
	TotalPnlMinor          int64         `json:"total_pnl_minor"`
	AccountTotalPnlMinor   int64         `json:"account_total_pnl_minor"`
	TimeWeightedReturnBps  int           `json:"time_weighted_return_bps"`
	PortfolioValueMinor    int64         `json:"portfolio_value_minor"`
	PortfolioValueHKDMinor int64         `json:"portfolio_value_hkd_minor"`
	ExchangeRateToHKD      float64       `json:"exchange_rate_to_hkd"`
	Trades                 []Trade       `json:"trades"`
	Holdings               []Holding     `json:"holdings"`
	CashBalances           []CashBalance `json:"cash_balances"`
	TransactionsValid      bool          `json:"transactions_valid"`
	HoldingsValid          bool          `json:"holdings_valid"`
	CashValid              bool          `json:"cash_valid"`
	Ready                  bool          `json:"ready"`
	ValidationErrors       []string      `json:"validation_errors"`
	ArtifactSHA256         string        `json:"artifact_sha256"`
}

type Trade struct {
	Symbol             string  `json:"symbol"`
	Name               string  `json:"name"`
	AssetType          string  `json:"asset_type"`
	UnderlyingSymbol   string  `json:"underlying_symbol"`
	ExpirationDate     string  `json:"expiration_date"`
	OptionType         string  `json:"option_type"`
	StrikePrice        float64 `json:"strike_price"`
	ContractMultiplier int     `json:"contract_multiplier"`
	Currency           string  `json:"currency"`
	TradeDate          string  `json:"trade_date"`
	TradeTime          string  `json:"trade_time"`
	SettlementDate     string  `json:"settlement_date"`
	Action             string  `json:"action"`
	Quantity           float64 `json:"quantity"`
	Price              float64 `json:"price"`
	GrossAmountMinor   int64   `json:"gross_amount_minor"`
	FeesMinor          int64   `json:"fees_minor"`
	TaxesMinor         int64   `json:"taxes_minor"`
	NetCashAmountMinor int64   `json:"net_cash_amount_minor"`
	RealizedPnlMinor   int64   `json:"realized_pnl_minor"`
	ExternalReference  string  `json:"external_reference"`
}

type Holding struct {
	Symbol             string  `json:"symbol"`
	Name               string  `json:"name"`
	AssetType          string  `json:"asset_type"`
	UnderlyingSymbol   string  `json:"underlying_symbol"`
	ExpirationDate     string  `json:"expiration_date"`
	OptionType         string  `json:"option_type"`
	StrikePrice        float64 `json:"strike_price"`
	ContractMultiplier int     `json:"contract_multiplier"`
	Currency           string  `json:"currency"`
	OpeningQuantity    float64 `json:"opening_quantity"`
	ClosingQuantity    float64 `json:"closing_quantity"`
	AverageCost        float64 `json:"average_cost"`
	CostBasisMinor     int64   `json:"cost_basis_minor"`
	ClosingPrice       float64 `json:"closing_price"`
	MarketValueMinor   int64   `json:"market_value_minor"`
	UnrealizedPnlMinor int64   `json:"unrealized_pnl_minor"`
}

type CashBalance struct {
	Currency    string  `json:"currency"`
	Opening     float64 `json:"opening"`
	Closing     float64 `json:"closing"`
	Deposits    float64 `json:"deposits"`
	Withdrawals float64 `json:"withdrawals"`
}

func ParsePDF(readerAt io.ReaderAt, size int64) (Statement, error) {
	return ParsePDFWithPassword(readerAt, size, "")
}

func ParsePDFWithPassword(readerAt io.ReaderAt, size int64, password string) (Statement, error) {
	return ParsePDFWithEngine(readerAt, size, password, EngineAuto)
}

func ParsePDFWithEngine(readerAt io.ReaderAt, size int64, password, engine string) (Statement, error) {
	data := make([]byte, size)
	if _, err := readerAt.ReadAt(data, 0); err != nil && !errors.Is(err, io.EOF) {
		return Statement{}, fmt.Errorf("读取 PDF: %w", err)
	}
	text, err := readablePDFText(data, password)
	if err != nil {
		return Statement{}, err
	}
	statement, err := ParseTextWithEngine(text, engine)
	if err != nil {
		return Statement{}, err
	}
	hash := sha256.Sum256(data)
	statement.ArtifactSHA256 = hex.EncodeToString(hash[:])
	return statement, nil
}

func readablePDFText(data []byte, password string) (string, error) {
	text, directErr := extractText(bytes.NewReader(data), int64(len(data)))
	if directErr == nil && hasUsefulText(text) {
		return text, nil
	}
	var decrypted bytes.Buffer
	model.ConfigPath = "disable"
	configuration := model.NewDefaultConfiguration()
	configuration.UserPW = password
	configuration.OwnerPW = password
	if decryptErr := pdfcpuapi.Decrypt(bytes.NewReader(data), &decrypted, configuration); decryptErr != nil {
		if password == "" {
			return "", fmt.Errorf("PDF 已加密且无法使用空密码读取: %w", decryptErr)
		}
		if directErr != nil {
			return "", fmt.Errorf("PDF 密码错误或不受支持: %w", decryptErr)
		}
		return "", fmt.Errorf("PDF 无法读取: %w", decryptErr)
	}
	return extractTextWithPoppler(decrypted.Bytes())
}

func extractTextWithPoppler(data []byte) (string, error) {
	binary := strings.TrimSpace(os.Getenv("HENGCAI_PDFTOTEXT_PATH"))
	if binary == "" {
		var err error
		binary, err = exec.LookPath("pdftotext")
		if err != nil {
			return "", errors.New("缺少 pdftotext；Docker 镜像需要安装 poppler-utils")
		}
	}
	file, err := os.CreateTemp("", "hengcai-statement-*.pdf")
	if err != nil {
		return "", fmt.Errorf("创建 PDF 临时文件: %w", err)
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return "", err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	command := exec.Command(binary, "-layout", "-enc", "UTF-8", name, "-")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("提取 PDF 文字失败: %w", err)
	}
	if strings.TrimSpace(string(output)) == "" {
		return "", errors.New("PDF 没有可读取的文字层")
	}
	return string(output), nil
}

func hasUsefulText(text string) bool {
	letters := 0
	for _, value := range text {
		if value > ' ' && value != '=' && (value < '0' || value > '9') {
			letters++
		}
	}
	return letters >= 20
}

func ParseText(text string) (Statement, error) {
	return ParseTextWithEngine(text, EngineAuto)
}

func ParseTextWithEngine(text, engine string) (Statement, error) {
	text = normalizeText(text)
	engine = strings.ToUpper(strings.TrimSpace(engine))
	if engine == "" {
		engine = EngineAuto
	}
	isHSBC := strings.Contains(text, "HSBC") && (strings.Contains(text, "Portfolio summary") || strings.Contains(text, "Transaction summary"))
	isIBKR := strings.Contains(text, "Interactive Brokers") && (strings.Contains(text, "活动账单") || strings.Contains(text, "Activity Statement"))
	isChief := strings.Contains(text, "Chief Securities Limited") && strings.Contains(text, "致富证券有限公司")
	isChiefGlobal := isChief && strings.Contains(text, "Global (Global)")
	isChiefHK := isChief && strings.Contains(text, "Local (HK)")

	switch engine {
	case EngineAuto:
		switch {
		case isHSBC:
			return parseHSBC(text)
		case isIBKR:
			return parseIBKR(text)
		case isChiefGlobal:
			return parseChief(text, true)
		case isChiefHK:
			return parseChief(text, false)
		default:
			return Statement{}, errors.New("无法识别对账单；请选择正确的券商识别引擎")
		}
	case EngineHSBC:
		if !isHSBC {
			return Statement{}, errors.New("所选汇丰识别引擎与 PDF 对账单不匹配")
		}
		return parseHSBC(text)
	case EngineIBKR:
		if !isIBKR {
			return Statement{}, errors.New("所选 IBKR 识别引擎与 PDF 对账单不匹配")
		}
		return parseIBKR(text)
	case EngineChiefHK:
		if !isChiefHK {
			return Statement{}, errors.New("所选致富证券香港识别引擎与 PDF 月结单不匹配")
		}
		return parseChief(text, false)
	case EngineChiefGlobal:
		if !isChiefGlobal {
			return Statement{}, errors.New("所选致富证券全球识别引擎与 PDF 月结单不匹配")
		}
		return parseChief(text, true)
	default:
		return Statement{}, fmt.Errorf("不支持的券商识别引擎: %s", engine)
	}
}

func extractText(readerAt io.ReaderAt, size int64) (string, error) {
	reader, err := pdf.NewReader(readerAt, size)
	if err != nil {
		return "", fmt.Errorf("打开 PDF: %w", err)
	}
	var out strings.Builder
	for pageNumber := 1; pageNumber <= reader.NumPage(); pageNumber++ {
		page := reader.Page(pageNumber)
		rows, rowErr := page.GetTextByRow()
		if rowErr != nil {
			return "", fmt.Errorf("读取 PDF 第 %d 页: %w", pageNumber, rowErr)
		}
		out.WriteString(fmt.Sprintf("\n=== PAGE %d ===\n", pageNumber))
		for _, row := range rows {
			parts := make([]string, 0, len(row.Content))
			for _, item := range row.Content {
				value := strings.TrimSpace(item.S)
				if value != "" {
					parts = append(parts, value)
				}
			}
			out.WriteString(strings.Join(parts, " "))
			out.WriteByte('\n')
		}
	}
	if strings.TrimSpace(out.String()) == "" {
		return "", errors.New("PDF 没有可读取的文字层")
	}
	return out.String(), nil
}

func normalizeText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "−", "-")
	text = strings.ReplaceAll(text, "–", "-")
	text = strings.ReplaceAll(text, "\u00a0", " ")
	text = regexp.MustCompile(`\t+`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(` +`).ReplaceAllString(text, " ")
	text = regexp.MustCompile(`\n{3,}`).ReplaceAllString(text, "\n\n")
	return strings.TrimSpace(text)
}

func parseNumber(raw string) (float64, error) {
	raw = strings.TrimSpace(strings.ReplaceAll(raw, ",", ""))
	negativeSuffix := strings.HasSuffix(raw, "-")
	raw = strings.TrimSuffix(raw, "-")
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, err
	}
	if negativeSuffix {
		value = -value
	}
	return value, nil
}

func minor(value float64) int64 { return int64(math.Round(value * 100)) }

func mustDate(raw, layout string) (string, error) {
	value, err := time.Parse(layout, strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	return value.Format("2006-01-02"), nil
}

func lineSlice(text, start, end string) string {
	startIndex := strings.Index(text, start)
	if startIndex < 0 {
		return ""
	}
	result := text[startIndex+len(start):]
	if end != "" {
		if endIndex := strings.Index(result, end); endIndex >= 0 {
			result = result[:endIndex]
		}
	}
	return result
}

func sortedSymbols(holdings []Holding) []string {
	values := make([]string, 0, len(holdings))
	for _, holding := range holdings {
		values = append(values, holding.Symbol)
	}
	sort.Strings(values)
	return values
}
