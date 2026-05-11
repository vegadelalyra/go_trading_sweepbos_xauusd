package main

import (
	"fmt"
	"time"
)

// TurtleSoup ───────────────────────────────────────────────────────────────
//
// Classical liquidity-fade against the PDH/PDL (Previous Day High / Low):
//   • Previous candle swept the level (its wick crossed PDH/PDL).
//   • Current candle closes back inside the previous day's range.
//
// This means retail breakout traders just got trapped on the wrong side
// and we fade them. SL parks just beyond the sweep wick; TP is 1:2 RR.
//
// Edge case learned from run #1: when the sweep wick is HUGE (>50pts in 15m),
// the structural SL is so far away that even a winning trade can take
// thousands of candles to resolve. That outlier risk is now handled by
// BacktestParams.MaxSLPoints + MaxHoldCandles — both enforced at the engine
// boundary, not here.

func TurtleSoup(candles []Candle, _ Analysis, i int) (Signal, bool) {
	if i < 96 {
		return Signal{}, false
	}
	pdh, pdl, ok := previousDayLevels(candles, i)
	if !ok {
		return Signal{}, false
	}
	cur := candles[i]
	prev := candles[i-1]

	// Bearish: prev wick swept PDH, current closes back below.
	if prev.High > pdh && cur.Close < pdh {
		entry := cur.Close
		sl := prev.High + slBuffer
		tp := tpFromRR(entry, sl, false)
		return buildSignal(
			"TurtleSoup", false, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("PDH=%.2f sweep=%.2f reclaim", pdh, prev.High),
		)
	}
	// Bullish: prev wick swept PDL, current closes back above.
	if prev.Low < pdl && cur.Close > pdl {
		entry := cur.Close
		sl := prev.Low - slBuffer
		tp := tpFromRR(entry, sl, true)
		return buildSignal(
			"TurtleSoup", true, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("PDL=%.2f sweep=%.2f reclaim", pdl, prev.Low),
		)
	}
	return Signal{}, false
}

// ── Strategy-local helpers ──────────────────────────────────────────────────

func previousDayLevels(candles []Candle, i int) (pdh, pdl float64, ok bool) {
	curDay := toNY(candles[i].Time).Truncate(24 * time.Hour)
	prevDay := curDay.Add(-24 * time.Hour)
	pdh = -1
	pdl = 1e18
	found := false
	for k := i - 1; k >= 0 && i-k < 200; k-- {
		d := toNY(candles[k].Time).Truncate(24 * time.Hour)
		if d.Equal(prevDay) {
			found = true
			if candles[k].High > pdh {
				pdh = candles[k].High
			}
			if candles[k].Low < pdl {
				pdl = candles[k].Low
			}
		} else if d.Before(prevDay) {
			break
		}
	}
	if !found {
		return 0, 0, false
	}
	return pdh, pdl, true
}
