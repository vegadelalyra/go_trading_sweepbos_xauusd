package main

import "fmt"

// LiquiditySweepBoS ───────────────────────────────────────────────────────
//
// The most distilled SMC trigger: liquidity sweep + structure break, with no
// session or CHoCh requirement. SilverBullet is a strict subset of this; we
// keep this strategy looser so we capture setups that fire outside kill
// zones (e.g. London genuine breakout, post-news continuation).
//
// Sequence:
//   • A BoS occurred within the previous ~6 candles (any direction).
//   • The BoS was preceded (within 8 candles) by a sweep of the opposite
//     swing extreme — the textbook stop run before the move.
// Entry: at current close (market). SL: just past the sweep wick.

func LiquiditySweepBoS(candles []Candle, a Analysis, i int) (Signal, bool) {
	if i < 30 {
		return Signal{}, false
	}
	cur := candles[i]

	for k := len(a.Structure) - 1; k >= 0; k-- {
		ev := a.Structure[k]
		if ev.Index >= i {
			continue
		}
		if i-ev.Index > 6 {
			break
		}
		if ev.Kind != StructBOSBull && ev.Kind != StructBOSBear {
			continue
		}

		bull := ev.Kind == StructBOSBull
		sweep, swept := findSweep(candles, a.Swings, ev.Index, !bull)
		if !swept {
			continue
		}
		entry := cur.Close
		var sl float64
		if bull {
			sl = sweep - slBuffer
		} else {
			sl = sweep + slBuffer
		}
		tp := tpFromRR(entry, sl, bull)
		return buildSignal(
			"LiquiditySweepBoS", bull, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("BoS@%d sweep=%.2f", ev.Index, sweep),
		)
	}
	return Signal{}, false
}
