package main

import (
	"fmt"
	"math"
)

// AsianRangeBreakout ──────────────────────────────────────────────────────
//
// Opposite hypothesis to Power-of-Three: sometimes the London open is a
// genuine breakout of the Asian range, not a manipulation. We trade in the
// direction of the impulsive candle that closes beyond the range, only in
// the first hours of London where this typically prints.
//
// Filter for impulse: |close − open| > 30% of the Asian range, so we skip
// drift-through. SL parks at the opposite extreme of the previous candle
// (whichever is closer), not at the full Asian range — that keeps the
// SL distance bounded for the engine's MaxSLPoints guard.

func AsianRangeBreakout(candles []Candle, _ Analysis, i int) (Signal, bool) {
	if i < 96 {
		return Signal{}, false
	}
	cur := candles[i]
	ny := toNY(cur.Time)
	h := ny.Hour()
	// Extended window: London open through NY open. Most of the day's
	// breakout impulse prints inside this band.
	if h < 2 || h >= 9 {
		return Signal{}, false
	}

	asiaHigh, asiaLow, ok := asianRange(candles, i)
	if !ok {
		return Signal{}, false
	}
	asiaSize := asiaHigh - asiaLow
	if asiaSize <= 0 {
		return Signal{}, false
	}

	// Relaxed: 15% body vs. the Asian range is enough; full 30% was filtering
	// out almost every real breakout candle (we got 1 trade in 2 years).
	bodySize := math.Abs(cur.Close - cur.Open)
	if bodySize < asiaSize*0.15 {
		return Signal{}, false
	}

	prev := candles[i-1]

	// Long breakout: close above Asian high. We no longer require open<high;
	// gap-up breakouts that open already beyond are still valid here.
	if cur.Close > asiaHigh && prev.Close <= asiaHigh {
		entry := cur.Close
		sl := prev.Low - slBuffer
		if sl >= entry {
			return Signal{}, false
		}
		tp := tpFromRR(entry, sl, true)
		return buildSignal(
			"AsianRangeBO", true, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("AsiaH=%.2f break=%.2f body=%.2f", asiaHigh, cur.Close, bodySize),
		)
	}
	if cur.Close < asiaLow && prev.Close >= asiaLow {
		entry := cur.Close
		sl := prev.High + slBuffer
		if sl <= entry {
			return Signal{}, false
		}
		tp := tpFromRR(entry, sl, false)
		return buildSignal(
			"AsianRangeBO", false, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("AsiaL=%.2f break=%.2f body=%.2f", asiaLow, cur.Close, bodySize),
		)
	}
	return Signal{}, false
}
