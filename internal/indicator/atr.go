// Package indicator holds the small, stateless price-series transforms used
// by both the engine (ATR widening) and strategies (EMA/RSI/Bollinger). The
// functions take raw slices and return same-length output slices so callers
// index directly by candle position without offset bookkeeping.
package indicator

import (
	"math"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
)

// TrueRange returns Wilder's True Range using prev.Close as the reference.
// Caller must handle the very first candle (no prev) via TrueRangeFirst.
func TrueRange(prev, cur core.Candle) float64 {
	hl := cur.High - cur.Low
	hc := math.Abs(cur.High - prev.Close)
	lc := math.Abs(cur.Low - prev.Close)
	return math.Max(hl, math.Max(hc, lc))
}

// TrueRangeFirst handles the seed candle where no prev close exists.
func TrueRangeFirst(cur core.Candle) float64 {
	return cur.High - cur.Low
}

// ATR returns Wilder's Average True Range over `period` for each candle.
// Indices 0..period-1 are 0 (not enough data). Index period-1 is the simple
// mean of the first `period` TRs (Wilder seed — not an EMA-style 2/(n+1) seed,
// Wilder uses 1/n smoothing which is equivalent to EMA with alpha = 1/period).
func ATR(candles []core.Candle, period int) []float64 {
	out := make([]float64, len(candles))
	if period <= 0 || len(candles) < period {
		return out
	}

	sum := TrueRangeFirst(candles[0])
	for i := 1; i < period; i++ {
		sum += TrueRange(candles[i-1], candles[i])
	}
	out[period-1] = sum / float64(period)

	pf := float64(period)
	for i := period; i < len(candles); i++ {
		tr := TrueRange(candles[i-1], candles[i])
		out[i] = (out[i-1]*(pf-1) + tr) / pf
	}
	return out
}

// ATRAt computes ATR at index i without allocating the full slice.
// Returns 0 if i is out of range or before the seed bar (period-1).
func ATRAt(candles []core.Candle, i, period int) float64 {
	if period <= 0 || i < period-1 || i >= len(candles) {
		return 0
	}

	sum := TrueRangeFirst(candles[0])
	for k := 1; k < period; k++ {
		sum += TrueRange(candles[k-1], candles[k])
	}
	atr := sum / float64(period)
	if i == period-1 {
		return atr
	}

	pf := float64(period)
	for k := period; k <= i; k++ {
		tr := TrueRange(candles[k-1], candles[k])
		atr = (atr*(pf-1) + tr) / pf
	}
	return atr
}
