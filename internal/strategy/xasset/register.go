package xasset

import "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"

// XAUXAGPairsInstance is the package singleton consumed by the legacy
// wrapper in main and by callers (CLI flag handler) that supply the XAG
// candle stream via SetXAG.
var XAUXAGPairsInstance = &XAUXAGPairs{Cfg: DefaultXAUXAGPairsConfig()}

// init registers the cross-asset strategy. It starts disabled — SetXAG must
// be called with a non-empty slice before any signal can fire.
func init() {
	strategy.Register(XAUXAGPairsInstance)
}
