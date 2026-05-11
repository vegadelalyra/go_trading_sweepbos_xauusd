package main

import "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/ta"

// BollingerReversal — legacy shim, see internal/strategy/ta.BollingerReversal.
func BollingerReversal(candles []Candle, a Analysis, i int) (Signal, bool) {
	return ta.BollingerReversalInstance.Detect(candles, a, i)
}
