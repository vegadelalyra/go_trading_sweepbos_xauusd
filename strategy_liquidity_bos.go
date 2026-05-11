package main

import smcstrat "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/smc"

// LiquiditySweepBoS — legacy shim, see internal/strategy/smc.LiquiditySweepBoS.
func LiquiditySweepBoS(candles []Candle, a Analysis, i int) (Signal, bool) {
	return smcstrat.LiquiditySweepBoSInstance.Detect(candles, a, i)
}
