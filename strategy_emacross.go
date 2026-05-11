package main

import "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/ta"

// EMACross — legacy shim, see internal/strategy/ta.EMACross.
func EMACross(candles []Candle, a Analysis, i int) (Signal, bool) {
	return ta.EMACrossInstance.Detect(candles, a, i)
}
