package main

import smcstrat "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/smc"

// BreakerBlock — legacy shim, see internal/strategy/smc.BreakerBlock.
func BreakerBlock(candles []Candle, a Analysis, i int) (Signal, bool) {
	return smcstrat.BreakerBlockInstance.Detect(candles, a, i)
}
