package main

import smcstrat "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/smc"

// AsianRangeBreakout — legacy shim, see internal/strategy/smc.AsianRangeBreakout.
func AsianRangeBreakout(candles []Candle, a Analysis, i int) (Signal, bool) {
	return smcstrat.AsianRangeBreakoutInstance.Detect(candles, a, i)
}
