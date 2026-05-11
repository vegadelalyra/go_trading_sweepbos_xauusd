package main

import "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/ta"

// DailyBreakout — legacy shim, see internal/strategy/ta.DailyBreakout.
func DailyBreakout(candles []Candle, a Analysis, i int) (Signal, bool) {
	return ta.DailyBreakoutInstance.Detect(candles, a, i)
}
