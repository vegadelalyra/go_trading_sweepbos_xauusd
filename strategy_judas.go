package main

import smcstrat "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/smc"

// JudasSwing — legacy shim, see internal/strategy/smc.JudasSwing.
func JudasSwing(candles []Candle, a Analysis, i int) (Signal, bool) {
	return smcstrat.JudasSwingInstance.Detect(candles, a, i)
}
