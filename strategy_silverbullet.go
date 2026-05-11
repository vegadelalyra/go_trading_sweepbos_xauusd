package main

import smcstrat "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/smc"

// SilverBullet — legacy shim, see internal/strategy/smc.SilverBullet.
// The kill-zone gate, sweep/CHoCh/FVG sequencing and the helpers that
// support them all live in the new package now.
func SilverBullet(candles []Candle, a Analysis, i int) (Signal, bool) {
	return smcstrat.SilverBulletInstance.Detect(candles, a, i)
}
