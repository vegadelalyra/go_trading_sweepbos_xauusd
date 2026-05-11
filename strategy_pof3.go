package main

import smcstrat "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/smc"

// PowerOfThree — legacy shim, see internal/strategy/smc.PowerOfThree.
// The asianRange helper that this and two siblings used lives in the new
// package now and is unexported (package-private), so the helper has been
// removed from package main.
func PowerOfThree(candles []Candle, a Analysis, i int) (Signal, bool) {
	return smcstrat.PowerOfThreeInstance.Detect(candles, a, i)
}
