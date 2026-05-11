package main

import "fmt"

// FVGFill ──────────────────────────────────────────────────────────────────
//
// Fair Value Gap fill: price returns to a recent FVG (price imbalance) and
// trades around its midpoint. We enter at the mid, with SL just beyond the
// far edge of the FVG.
//
// Quality gates (added after FVGFill exploded to 1814 trades in run #3
// once OrderBlockMit stopped intercepting candles):
//   • Skip micro FVGs (size < minFVGSize) — they're noise on 15m.
//   • Skip already-mitigated FVGs (any prior candle covered the midpoint).
//   • Walk back at most 30 candles; first match wins.

const minFVGSize = 2.0 // dollars

func FVGFill(candles []Candle, a Analysis, i int) (Signal, bool) {
	if i < 30 {
		return Signal{}, false
	}
	cur := candles[i]

	for k := len(a.FVGs) - 1; k >= 0; k-- {
		f := a.FVGs[k]
		if f.Index >= i {
			continue
		}
		age := i - f.Index
		if age > 30 {
			break
		}
		if age < 3 {
			continue
		}

		size := f.Top - f.Bottom
		if size < minFVGSize {
			continue
		}

		mid := f.Mid()
		mitigated := false
		for j := f.Index + 1; j < i; j++ {
			if candles[j].Low <= mid && candles[j].High >= mid {
				mitigated = true
				break
			}
		}
		if mitigated {
			continue
		}

		if cur.Low > f.Top || cur.High < f.Bottom {
			continue
		}

		bull := f.Bullish
		entry := mid
		var sl float64
		if bull {
			sl = f.Bottom - slBuffer
		} else {
			sl = f.Top + slBuffer
		}
		tp := tpFromRR(entry, sl, bull)
		return buildSignal(
			"FVGFill", bull, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("FVG@%d zone=%.2f-%.2f size=%.1f", f.Index, f.Bottom, f.Top, size),
		)
	}
	return Signal{}, false
}
