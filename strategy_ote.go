package main

import smcstrat "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/smc"

// OTEFibonacci — legacy shim, see internal/strategy/smc.OTEFibonacci.
func OTEFibonacci(candles []Candle, a Analysis, i int) (Signal, bool) {
	return smcstrat.OTEFibonacciInstance.Detect(candles, a, i)
}
