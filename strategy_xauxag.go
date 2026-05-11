package main

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// XAU/XAG Pairs (Gold-Silver Ratio) ─────────────────────────────────────────
//
// Mean-reversion play on the gold-to-silver price ratio. Historical extremes:
//   • Ratio > 90 → gold is rich vs silver → bias SHORT XAU.
//   • Ratio < 45 → gold is cheap vs silver → bias LONG XAU.
//
// Single-symbol constraint: this engine only takes XAU positions, so we use
// XAG purely as a signal generator (no pair leg is opened). That asymmetry
// means SL must be percent-based (a structural SL belongs to the spread, not
// either leg in isolation).

// XAGCandles holds the silver series aligned in time to the XAU candles
// passed to RunBacktest. Must be assigned before RunBacktest if XAUXAGPairs
// is in the strategy list; otherwise the strategy is a no-op.
var XAGCandles []Candle

const (
	ratioOverbought = 90.0 // gold too expensive → SHORT XAU
	ratioOversold   = 45.0 // gold too cheap     → LONG XAU
	ratioSLPct      = 0.02 // 2% from entry; ratio plays have no structural SL
)

func XAUXAGPairs(candles []Candle, _ Analysis, i int) (Signal, bool) {
	if i < 50 {
		return Signal{}, false
	}
	if len(XAGCandles) == 0 {
		return Signal{}, false
	}

	xag, ok := findMatchingXAG(candles[i].Time)
	if !ok {
		return Signal{}, false
	}
	if xag.Close == 0 {
		return Signal{}, false
	}

	ratio := candles[i].Close / xag.Close

	if ratio > ratioOverbought {
		entry := candles[i].Close
		sl := entry * (1 + ratioSLPct)
		tp := tpFromRR(entry, sl, false)
		return buildSignal(
			"XAUXAGPairs", false, entry, sl, tp, i, candles[i].Time,
			fmt.Sprintf("XAU/XAG ratio=%.1f (>%.0f)", ratio, ratioOverbought),
		)
	}

	if ratio < ratioOversold {
		entry := candles[i].Close
		sl := entry * (1 - ratioSLPct)
		tp := tpFromRR(entry, sl, true)
		return buildSignal(
			"XAUXAGPairs", true, entry, sl, tp, i, candles[i].Time,
			fmt.Sprintf("XAU/XAG ratio=%.1f (<%.0f)", ratio, ratioOversold),
		)
	}

	return Signal{}, false
}

// findMatchingXAG returns the silver candle whose Time is the largest <= t,
// requiring the gap to be within 4 hours. Beyond that we treat the series
// as having a data hole and skip rather than trade on stale silver.
func findMatchingXAG(t time.Time) (Candle, bool) {
	if len(XAGCandles) == 0 {
		return Candle{}, false
	}
	// sort.Search returns the smallest k where XAGCandles[k].Time > t.
	// The candidate is therefore at k-1 (largest Time <= t).
	k := sort.Search(len(XAGCandles), func(j int) bool {
		return XAGCandles[j].Time.After(t)
	})
	if k == 0 {
		return Candle{}, false
	}
	cand := XAGCandles[k-1]
	if math.Abs(t.Sub(cand.Time).Hours()) > 4 {
		return Candle{}, false
	}
	return cand, true
}
