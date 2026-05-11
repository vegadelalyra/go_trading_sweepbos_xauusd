package smc

import "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"

// DealingRange captures the high/low oscillation that ICT calls the "dealing
// range" — anything above the midpoint sits in Premium, anything below in
// Discount. Smart money sells Premium and buys Discount, so we use this as
// a coarse filter against retail signals fading the wrong half.
type DealingRange struct {
	High float64
	Low  float64
	Mid  float64 // = (High + Low) / 2
}

// CurrentDealingRange returns the dealing range derived from the last
// `lookback` candles ending at (exclusive of) index i. We use the simple
// highest-high / lowest-low rather than swing-based detection — it's good
// enough for the coarse Premium/Discount filter and lets the caller pick
// the granularity by changing lookback (96 candles = 24h on 15m).
func CurrentDealingRange(candles []core.Candle, i, lookback int) DealingRange {
	start := i - lookback
	if start < 0 {
		start = 0
	}
	high := candles[start].High
	low := candles[start].Low
	for k := start; k < i; k++ {
		if candles[k].High > high {
			high = candles[k].High
		}
		if candles[k].Low < low {
			low = candles[k].Low
		}
	}
	return DealingRange{High: high, Low: low, Mid: (high + low) / 2}
}

// InPremium reports whether price sits in the upper half of the dealing range.
func (dr DealingRange) InPremium(price float64) bool { return price > dr.Mid }

// InDiscount reports whether price sits in the lower half of the dealing range.
func (dr DealingRange) InDiscount(price float64) bool { return price < dr.Mid }
