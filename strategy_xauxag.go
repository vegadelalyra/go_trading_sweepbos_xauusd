package main

import "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/xasset"

// XAUXAGPairs — legacy shim, see internal/strategy/xasset.XAUXAGPairs.
// The XAG candle stream is supplied through SetXAG (called from the CLI
// flag handler) rather than the old package-level XAGCandles global.
func XAUXAGPairs(candles []Candle, a Analysis, i int) (Signal, bool) {
	return xasset.XAUXAGPairsInstance.Detect(candles, a, i)
}
