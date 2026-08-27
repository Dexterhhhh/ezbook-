package alpaca

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	APIKeyID     string
	APISecretKey string
	DataBaseURL  string
}

type Client struct {
	config Config
	http   *http.Client
}

type LatestBar struct {
	Symbol    string          `json:"-"`
	Timestamp time.Time       `json:"t"`
	Open      float64         `json:"o"`
	High      float64         `json:"h"`
	Low       float64         `json:"l"`
	Close     float64         `json:"c"`
	Volume    float64         `json:"v"`
	Raw       json.RawMessage `json:"-"`
}

func NewClient(config Config, httpClient *http.Client) (*Client, error) {
	if strings.TrimSpace(config.APIKeyID) == "" || strings.TrimSpace(config.APISecretKey) == "" {
		return nil, errors.New("Alpaca API Key ID 和 Secret Key 不能为空")
	}
	if strings.TrimSpace(config.DataBaseURL) == "" {
		return nil, errors.New("Alpaca Market Data API 地址不能为空")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	config.DataBaseURL = normalizeBaseURL(config.DataBaseURL)
	return &Client{config: config, http: httpClient}, nil
}

// Accept either a host or an Alpaca URL ending in /v2. The request methods
// append /v2 themselves, so retaining the suffix would create /v2/v2 paths.
func normalizeBaseURL(raw string) string {
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if strings.HasSuffix(strings.ToLower(base), "/v2") {
		base = strings.TrimRight(base[:len(base)-3], "/")
	}
	return base
}

func (c *Client) LatestStockBar(ctx context.Context, symbol, feed string) (LatestBar, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return LatestBar{}, errors.New("股票代码不能为空")
	}
	query := url.Values{}
	if strings.TrimSpace(feed) != "" {
		query.Set("feed", strings.TrimSpace(feed))
	}
	endpoint := fmt.Sprintf("%s/v2/stocks/%s/bars/latest", strings.TrimRight(c.config.DataBaseURL, "/"), url.PathEscape(symbol))
	if encoded := query.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}
	var payload struct {
		Bar json.RawMessage `json:"bar"`
	}
	if err := c.getJSON(ctx, endpoint, &payload); err != nil {
		return LatestBar{}, err
	}
	if len(payload.Bar) == 0 || string(payload.Bar) == "null" {
		return LatestBar{}, fmt.Errorf("Alpaca 未返回 %s 的最新行情", symbol)
	}
	var bar LatestBar
	if err := json.Unmarshal(payload.Bar, &bar); err != nil {
		return LatestBar{}, fmt.Errorf("解析 %s 行情失败: %w", symbol, err)
	}
	bar.Symbol = symbol
	bar.Raw = append(json.RawMessage(nil), payload.Bar...)
	if bar.Close < 0 || bar.Timestamp.IsZero() {
		return LatestBar{}, fmt.Errorf("Alpaca 返回的 %s 行情数据无效", symbol)
	}
	return bar, nil
}

// LatestDailyBar returns the most recent completed daily bar. Alpaca defaults
// start to the current day when it is omitted, so sending only an end at the
// beginning of today can produce "end should not be before start". Use an
// explicit lookback window and stop at the beginning of the current UTC day.
func (c *Client) LatestDailyBar(ctx context.Context, symbol, feed string) (LatestBar, error) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if symbol == "" {
		return LatestBar{}, errors.New("股票代码不能为空")
	}
	query := url.Values{}
	query.Set("timeframe", "1Day")
	query.Set("limit", "1")
	query.Set("sort", "desc")
	end := time.Now().UTC().Truncate(24 * time.Hour)
	start := end.AddDate(0, 0, -14)
	query.Set("start", start.Format(time.RFC3339))
	query.Set("end", end.Format(time.RFC3339))
	if strings.TrimSpace(feed) != "" {
		query.Set("feed", strings.TrimSpace(feed))
	}
	endpoint := fmt.Sprintf("%s/v2/stocks/%s/bars?%s", strings.TrimRight(c.config.DataBaseURL, "/"), url.PathEscape(symbol), query.Encode())
	var payload struct {
		Bars []LatestBar `json:"bars"`
	}
	if err := c.getJSON(ctx, endpoint, &payload); err != nil {
		return LatestBar{}, err
	}
	if len(payload.Bars) == 0 {
		return LatestBar{}, fmt.Errorf("Alpaca 未返回 %s 的已收盘日线行情", symbol)
	}
	bar := payload.Bars[0]
	bar.Symbol = symbol
	if bar.Close < 0 || bar.Timestamp.IsZero() {
		return LatestBar{}, fmt.Errorf("Alpaca 返回的 %s 收盘行情数据无效", symbol)
	}
	if data, err := json.Marshal(bar); err == nil {
		bar.Raw = data
	}
	return bar, nil
}

// getJSON deliberately exposes only GET requests to the Alpaca Market Data
// host. This extension has no account, order, or trading operations.
func (c *Client) getJSON(ctx context.Context, endpoint string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("APCA-API-KEY-ID", c.config.APIKeyID)
	req.Header.Set("APCA-API-SECRET-KEY", c.config.APISecretKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("请求 Alpaca 失败: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return fmt.Errorf("读取 Alpaca 响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := strings.TrimSpace(string(data))
		if message == "" {
			message = resp.Status
		}
		return fmt.Errorf("Alpaca 返回 HTTP %d: %s", resp.StatusCode, message)
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("解析 Alpaca 响应失败: %w", err)
	}
	return nil
}
