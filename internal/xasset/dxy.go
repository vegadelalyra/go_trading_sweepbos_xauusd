// Package xasset hosts cross-asset structural analyses that the meta
// layer consumes to weight XAUUSD signals against macro flow. The
// flagship member is DXY structure classification — see ClassifyDXYStructure.
//
// The package is deliberately framework-free: it depends only on core.
// Strategy registration for the cross-asset XAUXAGPairs sits in a
// separate package (internal/strategy/xasset) to avoid an import cycle.
package xasset

import (
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
)

// DXYStructure encodes the dollar-index's structural posture over the
// recent window. Used by the meta layer to penalise XAU signals fighting
// macro flow and boost confluent ones.
//
// Microstructural rationale: XAU has a strong inverse correlation with
// DXY. When DXY prints HH+HL (uptrend), an XAU LONG is fighting the
// macro flow — that long is statistically more likely to fade. When DXY
// prints LL+LH (downtrend), the same XAU LONG has confluence with macro
// flow. Neutral DXY (any mixed pattern) carries no informational edge
// and yields a 1.0 multiplier downstream.
type DXYStructure int

const (
	// DXYNeutral: no clean HH+HL or LL+LH pattern in the window. Default
	// zero-value so a missing lookup falls through to "no adjustment".
	DXYNeutral DXYStructure = iota
	// DXYBullish: both the most recent swing high and the most recent
	// swing low printed HIGHER than their predecessors.
	DXYBullish
	// DXYBearish: both the most recent swing high and the most recent
	// swing low printed LOWER than their predecessors.
	DXYBearish
)

// String renders the structure for logs / debug output.
func (s DXYStructure) String() string {
	switch s {
	case DXYBullish:
		return "BULLISH"
	case DXYBearish:
		return "BEARISH"
	default:
		return "NEUTRAL"
	}
}

// ClassifyDXYStructure looks at the last n candles of `candles` and
// returns the structural posture. The detector walks the slice and
// records 3-bar swing highs (a bar whose High is strictly greater than
// both neighbours' Highs) and 3-bar swing lows (mirror condition on
// Lows). It then compares the two most-recent swings on each side:
//
//   - HH (latest swing high > prior swing high) AND
//     HL (latest swing low  > prior swing low ) → DXYBullish
//   - LL (latest swing high < prior swing high) AND
//     LH (latest swing low  < prior swing low ) → DXYBearish
//   - anything else (mixed / not enough swings)      → DXYNeutral
//
// The function is pure — no allocations beyond two small swing slices,
// no time-of-day awareness. Callers cache the result per DXY bar at
// startup; runtime cost per XAU candle is then an O(1) map lookup.
func ClassifyDXYStructure(candles []core.Candle, n int) DXYStructure {
	if n <= 0 || len(candles) < 5 {
		return DXYNeutral
	}
	// Restrict to the last n bars. A 3-bar swing pattern needs at
	// minimum 5 bars to produce two distinct swings on the same side
	// (swing₁ at idx 1, swing₂ at idx 3 — both with valid neighbours).
	start := len(candles) - n
	if start < 0 {
		start = 0
	}
	window := candles[start:]
	if len(window) < 5 {
		return DXYNeutral
	}

	var highs, lows []float64
	// Skip the first and last index of the window — both lack a complete
	// 3-bar neighbourhood. Strict inequality ensures we don't double-count
	// flat tops/bottoms.
	for i := 1; i < len(window)-1; i++ {
		h := window[i].High
		if h > window[i-1].High && h > window[i+1].High {
			highs = append(highs, h)
		}
		l := window[i].Low
		if l < window[i-1].Low && l < window[i+1].Low {
			lows = append(lows, l)
		}
	}
	if len(highs) < 2 || len(lows) < 2 {
		return DXYNeutral
	}

	// Compare the TWO most-recent swings on each side.
	lhHi, hhHi := highs[len(highs)-2], highs[len(highs)-1]
	lhLo, hhLo := lows[len(lows)-2], lows[len(lows)-1]

	hh := hhHi > lhHi
	hl := hhLo > lhLo
	ll := hhHi < lhHi
	lh := hhLo < lhLo

	switch {
	case hh && hl:
		return DXYBullish
	case ll && lh:
		return DXYBearish
	default:
		return DXYNeutral
	}
}
