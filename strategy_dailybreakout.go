package main

import (
	"fmt"
	"math"
)

// DailyBreakout ────────────────────────────────────────────────────────────
//
// Donchian-style breakout: price closes beyond the 20-bar high/low range
// after sitting inside it the previous bar, and the breakout candle has an
// impulsive body (≥30% of the prior range). SL sits at the opposite edge of
// the broken range. Captures the start of new trending legs; the impulse
// filter screens out limp closes that mostly become traps.

const (
	dbLookback     = 20
	dbImpulseRatio = 0.30
)

func DailyBreakout(candles []Candle, a Analysis, i int) (Signal, bool) {
	if i < dbLookback {
		return Signal{}, false
	}

	// Window candles[i-dbLookback : i] — strictly prior to current candle.
	prevHigh := candles[i-dbLookback].High
	prevLow := candles[i-dbLookback].Low
	for k := i - dbLookback + 1; k < i; k++ {
		if candles[k].High > prevHigh {
			prevHigh = candles[k].High
		}
		if candles[k].Low < prevLow {
			prevLow = candles[k].Low
		}
	}

	rangeSize := prevHigh - prevLow
	if rangeSize <= 0 {
		return Signal{}, false
	}

	cur := candles[i]
	bodySize := math.Abs(cur.Close - cur.Open)
	if bodySize < rangeSize*dbImpulseRatio {
		return Signal{}, false
	}

	bullBO := cur.Close > prevHigh && candles[i-1].Close <= prevHigh
	bearBO := cur.Close < prevLow && candles[i-1].Close >= prevLow
	if !bullBO && !bearBO {
		return Signal{}, false
	}

	bull := bullBO
	entry := cur.Close
	var sl float64
	var reason string
	if bull {
		sl = prevLow - slBuffer
		if sl >= entry {
			return Signal{}, false
		}
		reason = fmt.Sprintf("%d-bar BO above %.2f", dbLookback, prevHigh)
	} else {
		sl = prevHigh + slBuffer
		if sl <= entry {
			return Signal{}, false
		}
		reason = fmt.Sprintf("%d-bar BO below %.2f", dbLookback, prevLow)
	}
	tp := tpFromRR(entry, sl, bull)

	return buildSignal("DailyBreakout", bull, entry, sl, tp, i, cur.Time, reason)
}
