package main

import (
	"sort"
	"time"
)

// biasPoint binds a UTC bar timestamp to its integer bias label.
// Stored in a slice (not a map) so lookups can binary-search by time.
type biasPoint struct {
	T    time.Time
	Bias int
}

// BiasMap is the public type returned by BuildBiasMap. It exposes Lookup(t)
// for O(log n) "what was the H4 trend at time t?" queries. The backing
// representation is intentionally unexported so callers cannot mutate it.
type BiasMap struct {
	points []biasPoint
}

// EMA computes the exponential moving average over values with the given period.
// The output slice has the same length as values; the first (period-1) entries
// are 0 (not yet computable). values[period-1] is seeded with a Simple Moving
// Average of the first `period` items — this matches the convention used by
// trading platforms (TradingView, MT4/5) and avoids the cold-start bias of a
// pure recursive EMA started from a single sample.
func EMA(values []float64, period int) []float64 {
	out := make([]float64, len(values))
	if period <= 0 || len(values) < period {
		return out // not enough data to seed the SMA — leave everything at 0.
	}
	// SMA seed: average of the first `period` closes.
	var sum float64
	for i := 0; i < period; i++ {
		sum += values[i]
	}
	out[period-1] = sum / float64(period)
	// Standard EMA smoothing factor; period+1 is the canonical denominator.
	k := 2.0 / float64(period+1)
	for i := period; i < len(values); i++ {
		out[i] = (values[i] * k) + (out[i-1] * (1 - k))
	}
	return out
}

// BuildBiasMap derives a per-bar trend bias from H4 candles using EMA(emaPeriod)
// of the close. Bias is +1 (close above EMA), -1 (below), or 0 (equal/unseeded).
// The result is sorted by Time so Lookup can binary-search; we assume the input
// is already chronological (loadCandles ORDER BY bar_time ASC) but sort defensively.
func BuildBiasMap(h4Candles []Candle, emaPeriod int) BiasMap {
	n := len(h4Candles)
	if n == 0 || emaPeriod <= 0 {
		return BiasMap{points: nil}
	}
	closes := make([]float64, n)
	for i, c := range h4Candles {
		closes[i] = c.Close
	}
	ema := EMA(closes, emaPeriod)

	points := make([]biasPoint, 0, n)
	for i := 0; i < n; i++ {
		// Skip bars before the SMA seed lands — EMA is still 0 / unreliable there.
		if i < emaPeriod-1 {
			continue
		}
		b := 0
		switch {
		case h4Candles[i].Close > ema[i]:
			b = 1
		case h4Candles[i].Close < ema[i]:
			b = -1
		}
		// Normalize to UTC so Lookup(t.UTC()) and Lookup(t.Local()) match.
		points = append(points, biasPoint{T: h4Candles[i].Time.UTC(), Bias: b})
	}
	// Defensive sort: cheap (already-sorted input is O(n) for sort.SliceStable).
	sort.SliceStable(points, func(i, j int) bool {
		return points[i].T.Before(points[j].T)
	})
	return BiasMap{points: points}
}

// Lookup returns the bias of the most recent H4 bar whose Time <= t.
// Returns 0 when t precedes every bar in the map (or the map is empty),
// matching the "neutral / unknown" contract documented at the call sites.
func (bm BiasMap) Lookup(t time.Time) int {
	if len(bm.points) == 0 {
		return 0
	}
	tu := t.UTC()
	// sort.Search returns the first index whose T is strictly after tu.
	// The bar that "covers" tu is therefore at idx-1.
	idx := sort.Search(len(bm.points), func(i int) bool {
		return bm.points[i].T.After(tu)
	})
	if idx == 0 {
		return 0 // t is before the first known bar — no bias available.
	}
	return bm.points[idx-1].Bias
}
