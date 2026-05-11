package main

import "fmt"

// PowerOfThree (AMD: Accumulation–Manipulation–Distribution) ───────────────
//
// Frames the day in three legs:
//   • Asia (19:00 prev EST → 02:00 EST): accumulation, build the range.
//   • London (02:00–05:00 EST): manipulation, sweep one extreme.
//   • NY (07:00 onward): distribution, real direction emerges.
//
// We trigger on the manipulation leg — a candle in the last ~6 broke one
// side of the Asian range, then the current candle closed back inside.
// That reclaim defines the distribution direction we trade.

func PowerOfThree(candles []Candle, _ Analysis, i int) (Signal, bool) {
	if i < 96 {
		return Signal{}, false
	}
	cur := candles[i]
	ny := toNY(cur.Time)
	h := ny.Hour()
	if h < 2 || h >= 5 {
		return Signal{}, false
	}
	asiaHigh, asiaLow, ok := asianRange(candles, i)
	if !ok {
		return Signal{}, false
	}

	manipHigh, manipLow := -1.0, 1e18
	for k := i - 6; k <= i-1; k++ {
		if k < 0 {
			continue
		}
		if candles[k].High > manipHigh {
			manipHigh = candles[k].High
		}
		if candles[k].Low < manipLow {
			manipLow = candles[k].Low
		}
	}

	// Short: London swept Asia high, current reclaims below.
	if manipHigh > asiaHigh && cur.Close < asiaHigh {
		entry := cur.Close
		sl := manipHigh + slBuffer
		tp := tpFromRR(entry, sl, false)
		return buildSignal(
			"PowerOfThree", false, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("AsiaH=%.2f manip=%.2f", asiaHigh, manipHigh),
		)
	}
	// Long: London swept Asia low, current reclaims above.
	if manipLow < asiaLow && cur.Close > asiaLow {
		entry := cur.Close
		sl := manipLow - slBuffer
		tp := tpFromRR(entry, sl, true)
		return buildSignal(
			"PowerOfThree", true, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("AsiaL=%.2f manip=%.2f", asiaLow, manipLow),
		)
	}
	return Signal{}, false
}

// ── Strategy-local helpers ──────────────────────────────────────────────────

func asianRange(candles []Candle, i int) (high, low float64, ok bool) {
	high = -1
	low = 1e18
	count := 0
	for k := i - 1; k >= 0 && i-k < 96; k-- {
		ny := toNY(candles[k].Time)
		h := ny.Hour()
		inAsia := h >= 19 || h < 2
		if !inAsia {
			if count > 0 {
				break
			}
			continue
		}
		count++
		if candles[k].High > high {
			high = candles[k].High
		}
		if candles[k].Low < low {
			low = candles[k].Low
		}
	}
	if count < 4 {
		return 0, 0, false
	}
	return high, low, true
}
