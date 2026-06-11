// Package bias derives a higher-timeframe trend bias from an EMA of closes
// and exposes it through a time-indexed BiasMap so per-candle decisions can
// ask "what trend was in force at time t?" in O(log n).
package bias

import (
	"sort"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/indicator"
)

// biasPoint binds a UTC bar close-time to its integer bias label. Stored in a
// slice (not a map) so lookups can binary-search by time.
type biasPoint struct {
	T    time.Time
	Bias int
}

// BiasMap is the public type returned by BuildBiasMap. It exposes Lookup(t)
// for "what was the H4 trend at time t?" queries. The backing representation
// is intentionally unexported so callers cannot mutate it.
type BiasMap struct {
	points []biasPoint
}

// BuildBiasMap derives a per-bar trend bias from H4 candles using EMA(emaPeriod)
// of the close. Bias is +1 (close above EMA), -1 (below), or 0 (equal/unseeded).
//
// barDuration is the span of one input candle (e.g. 4*time.Hour for H4). Each
// bias point is indexed by its CLOSE time (open + barDuration) so Lookup(t)
// will never return a bar that hasn't finished printing by t. Without this,
// a 15m candle at 11:45 would consult the H4 bar that opened at 08:00 — which
// doesn't close until 12:00 — leaking the next 15 minutes of the H4 close
// (and therefore the EMA cross direction) into a "live" decision.
func BuildBiasMap(h4Candles []core.Candle, emaPeriod int, barDuration time.Duration) BiasMap {
	n := len(h4Candles)
	if n == 0 || emaPeriod <= 0 {
		return BiasMap{points: nil}
	}
	closes := make([]float64, n)
	for i, c := range h4Candles {
		closes[i] = c.Close
	}
	ema := indicator.EMA(closes, emaPeriod)

	points := make([]biasPoint, 0, n)
	for i := 0; i < n; i++ {
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
		points = append(points, biasPoint{T: h4Candles[i].Time.UTC().Add(barDuration), Bias: b})
	}
	sort.SliceStable(points, func(i, j int) bool {
		return points[i].T.Before(points[j].T)
	})
	return BiasMap{points: points}
}

// Lookup returns the bias of the most recent H4 bar that has already CLOSED by t.
// Because BuildBiasMap indexes points by close-time, the same binary search
// gives the no-leak answer: any bar still open at t sits after t in the slice
// and is excluded. Returns 0 when no closed bar precedes t (or the map is empty).
func (bm BiasMap) Lookup(t time.Time) int {
	if len(bm.points) == 0 {
		return 0
	}
	tu := t.UTC()
	idx := sort.Search(len(bm.points), func(i int) bool {
		return bm.points[i].T.After(tu)
	})
	if idx == 0 {
		return 0
	}
	return bm.points[idx-1].Bias
}
