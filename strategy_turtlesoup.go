package main

import smcstrat "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/smc"

// TurtleSoup — legacy shim, see internal/strategy/smc.TurtleSoup.
func TurtleSoup(candles []Candle, a Analysis, i int) (Signal, bool) {
	return smcstrat.TurtleSoupInstance.Detect(candles, a, i)
}
