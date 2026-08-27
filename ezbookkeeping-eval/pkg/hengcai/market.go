package hengcai

import (
	"context"

	"github.com/mayswind/ezbookkeeping/pkg/hengcai/alpaca"
)

// PriceProvider is the seam used by the private investment module. Alpaca is
// the bundled implementation, while a broker, exchange or local test feed can
// implement the same interface without changing investment tables or APIs.
type PriceProvider interface {
	LatestStockBar(ctx context.Context, symbol string, feed string) (alpaca.LatestBar, error)
}

type AlpacaPriceProvider struct{ Client *alpaca.Client }

func (p AlpacaPriceProvider) LatestStockBar(ctx context.Context, symbol string, feed string) (alpaca.LatestBar, error) {
	return p.Client.LatestStockBar(ctx, symbol, feed)
}
