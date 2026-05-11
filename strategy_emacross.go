package main

import "fmt"

// EMACross ─────────────────────────────────────────────────────────────────
//
// Classic trend-following entry: EMA50 crosses EMA200 while RSI confirms
// momentum on the same side (RSI ≥ 55 for longs, ≤ 45 for shorts). SL parks
// just beyond the recent swing (10-bar low/high) so we exit if the trend
// thesis is invalidated. TP is the fixed 1:2 RR. Few signals, but they ride
// the macro structure rather than fighting it.

const (
	emaFastPeriod      = 50
	emaSlowPeriod      = 200
	rsiPeriod          = 14
	rsiLongMin         = 55.0
	rsiShortMax        = 45.0
	emaCrossLookbackSL = 10
)

func EMACross(candles []Candle, a Analysis, i int) (Signal, bool) {
	if i < emaSlowPeriod+5 {
		return Signal{}, false
	}

	closes := make([]float64, i+1)
	for k := 0; k <= i; k++ {
		closes[k] = candles[k].Close
	}

	emaFast := EMA(closes, emaFastPeriod)
	emaSlow := EMA(closes, emaSlowPeriod)
	rsi := RSI(closes, rsiPeriod)

	bullCross := emaFast[i] > emaSlow[i] && emaFast[i-1] <= emaSlow[i-1]
	bearCross := emaFast[i] < emaSlow[i] && emaFast[i-1] >= emaSlow[i-1]
	if !bullCross && !bearCross {
		return Signal{}, false
	}

	bull := bullCross
	if bull && rsi[i] < rsiLongMin {
		return Signal{}, false
	}
	if !bull && rsi[i] > rsiShortMax {
		return Signal{}, false
	}

	entry := candles[i].Close
	var sl float64
	if bull {
		lo := candles[i].Low
		for k := i - emaCrossLookbackSL; k <= i; k++ {
			if k < 0 {
				continue
			}
			if candles[k].Low < lo {
				lo = candles[k].Low
			}
		}
		sl = lo - slBuffer
	} else {
		hi := candles[i].High
		for k := i - emaCrossLookbackSL; k <= i; k++ {
			if k < 0 {
				continue
			}
			if candles[k].High > hi {
				hi = candles[k].High
			}
		}
		sl = hi + slBuffer
	}
	tp := tpFromRR(entry, sl, bull)

	reason := fmt.Sprintf("EMA%d/%d cross + RSI=%.1f", emaFastPeriod, emaSlowPeriod, rsi[i])
	return buildSignal("EMACross", bull, entry, sl, tp, i, candles[i].Time, reason)
}
