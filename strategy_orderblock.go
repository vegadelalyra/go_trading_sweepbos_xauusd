package main

import smcstrat "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/smc"

// OrderBlockMitigation is the legacy function-style entry point retained so
// the in-main Strategies slice keeps working until A.8 retires it. The
// canonical implementation lives in internal/strategy/smc.OrderBlockMit.
func OrderBlockMitigation(candles []Candle, a Analysis, i int) (Signal, bool) {
	return smcstrat.OrderBlockMitInstance.Detect(candles, a, i)
}
