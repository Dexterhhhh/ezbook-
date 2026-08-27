package alpaca

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestLatestStockBar(t *testing.T) {
	client, err := NewClient(Config{
		APIKeyID: "test-key", APISecretKey: "test-secret",
		DataBaseURL: "https://example.test",
	}, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("APCA-API-KEY-ID") != "test-key" || r.Header.Get("APCA-API-SECRET-KEY") != "test-secret" {
			t.Fatalf("missing Alpaca auth headers")
		}
		if r.URL.Path != "/v2/stocks/AAPL/bars/latest" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"bar":{"t":"2026-07-30T01:02:03Z","o":100.1,"h":101.2,"l":99.9,"c":100.8,"v":42}}`)),
			Header:     make(http.Header),
		}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	bar, err := client.LatestStockBar(context.Background(), "aapl", "iex")
	if err != nil {
		t.Fatal(err)
	}
	if bar.Symbol != "AAPL" || bar.Close != 100.8 || bar.Timestamp.IsZero() {
		t.Fatalf("unexpected bar: %#v", bar)
	}
}

func TestLatestDailyBarNormalizesV2AndUsesCompletedClose(t *testing.T) {
	client, err := NewClient(Config{
		APIKeyID: "test-key", APISecretKey: "test-secret",
		DataBaseURL: "https://data.alpaca.markets/v2",
	}, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v2/stocks/SPY/bars" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.URL.Query().Get("timeframe") != "1Day" || r.URL.Query().Get("limit") != "1" || r.URL.Query().Get("sort") != "desc" {
			t.Fatalf("unexpected daily query %s", r.URL.RawQuery)
		}
		start, startErr := time.Parse(time.RFC3339, r.URL.Query().Get("start"))
		end, endErr := time.Parse(time.RFC3339, r.URL.Query().Get("end"))
		if startErr != nil || endErr != nil || !start.Before(end) {
			t.Fatalf("invalid daily query window start=%q end=%q", r.URL.Query().Get("start"), r.URL.Query().Get("end"))
		}
		if end.Sub(start) != 14*24*time.Hour {
			t.Fatalf("unexpected daily query window %s", end.Sub(start))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"bars":[{"t":"2026-07-30T00:00:00Z","o":100.1,"h":101.2,"l":99.9,"c":100.8,"v":42}]}`)),
			Header:     make(http.Header),
		}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	bar, err := client.LatestDailyBar(context.Background(), "spy", "iex")
	if err != nil {
		t.Fatal(err)
	}
	if bar.Symbol != "SPY" || bar.Close != 100.8 || bar.Timestamp.IsZero() {
		t.Fatalf("unexpected daily bar: %#v", bar)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
