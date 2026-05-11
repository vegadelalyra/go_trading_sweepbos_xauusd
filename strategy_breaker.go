package main

import "fmt"

// BreakerBlock ────────────────────────────────────────────────────────────
//
// A breaker is an Order Block that was broken by a CHoCh (a failed OB):
//   1. A bullish OB existed.
//   2. Price closed through it on a bearish CHoCh → the OB "failed".
//   3. Price later retests the broken OB from above, which now acts as
//      a supply zone for shorts.
//
// We trade the retest: the broken OB's zone becomes our entry; SL parks
// just beyond the far side of that zone.

func BreakerBlock(candles []Candle, a Analysis, i int) (Signal, bool) {
	if i < 50 {
		return Signal{}, false
	}
	cur := candles[i]

	for k := len(a.Structure) - 1; k >= 0; k-- {
		ev := a.Structure[k]
		if ev.Index >= i {
			continue
		}
		if i-ev.Index > 50 {
			break
		}
		choCh := ev.Kind == StructCHoChBull || ev.Kind == StructCHoChBear
		if !choCh {
			continue
		}
		isChoChBull := ev.Kind == StructCHoChBull

		// Look for the most recent OB that was broken by this CHoCh.
		// "Broken" = an OB of the OPPOSITE direction sitting just before the CHoCh,
		// within a wider lookback so we catch slower-developing breakers.
		var broken *OrderBlock
		for j := len(a.OrderBlocks) - 1; j >= 0; j-- {
			ob := a.OrderBlocks[j]
			if ob.Index >= ev.Index {
				continue
			}
			// The breaker setup requires the OB to have ALREADY existed before
			// the CHoCh broke it. If the OB's own confirming event sits at or
			// after ev, the OB was never an independent prior structure for
			// price to violate — we'd be inventing the breaker from future info.
			if ob.ResolveIdx >= ev.Index {
				continue
			}
			if ev.Index-ob.Index > 50 {
				break
			}
			if ob.Bullish == !isChoChBull {
				broken = &a.OrderBlocks[j]
				break
			}
		}
		if broken == nil {
			continue
		}

		// Retest tolerance: either the candle range touches the zone, OR the
		// candle's close is within half-the-zone-size from either edge.
		// This catches near-misses where price approaches but doesn't pierce.
		zoneSize := broken.Top - broken.Bottom
		tolerance := zoneSize * 0.5
		retest := !(cur.Low > broken.Top || cur.High < broken.Bottom)
		near := cur.Close >= broken.Bottom-tolerance && cur.Close <= broken.Top+tolerance
		if !retest && !near {
			continue
		}

		// Breaker direction equals the CHoCh direction.
		bull := isChoChBull
		entry := (broken.Top + broken.Bottom) / 2
		var sl float64
		if bull {
			sl = broken.Bottom - slBuffer
		} else {
			sl = broken.Top + slBuffer
		}
		tp := tpFromRR(entry, sl, bull)
		return buildSignal(
			"BreakerBlock", bull, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("CHoCh@%d brokenOB@%d", ev.Index, broken.Index),
		)
	}
	return Signal{}, false
}
