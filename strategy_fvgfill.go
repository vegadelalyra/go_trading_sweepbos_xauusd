package main

import smcstrat "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/smc"

// FVGFill — legacy shim, see internal/strategy/smc.FVGFill.
func FVGFill(candles []Candle, a Analysis, i int) (Signal, bool) {
	return smcstrat.FVGFillInstance.Detect(candles, a, i)
}
