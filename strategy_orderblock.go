package main

import "fmt"

// OrderBlockMitigation ─────────────────────────────────────────────────────
//
// Price returns to an unmitigated Order Block produced by an earlier BoS.
// We enter limit-style at the OB midpoint, parking SL just beyond its far
// edge. This is the canonical "mitigation" ICT setup: smart money left an
// imbalance at the OB and price comes back to fill it before continuing.
//
// Quality gates (learned from run #2's 1873-trade flood):
//   • Skip micro-OBs whose vertical size < minOBSize. They produce noise.
//   • Only the freshest still-unmitigated OB counts. If any candle between
//     ob.Index and i already covered the OB midpoint, the imbalance is gone
//     and we don't double-dip.
//   • Take the first candle that touches the zone — never re-trade the same
//     OB after age > 50 candles.

const minOBSize = 3.0 // dollars; tweakable per interval if needed

func OrderBlockMitigation(candles []Candle, a Analysis, i int) (Signal, bool) {
	if i < 30 {
		return Signal{}, false
	}
	cur := candles[i]

	for k := len(a.OrderBlocks) - 1; k >= 0; k-- {
		ob := a.OrderBlocks[k]
		if ob.Index >= i {
			continue
		}
		age := i - ob.Index
		if age > 50 {
			break
		}
		if age < 5 {
			continue
		}

		// Filter: skip tiny OBs (noise).
		size := ob.Top - ob.Bottom
		if size < minOBSize {
			continue
		}

		// Filter: ensure the OB midpoint hasn't been mitigated already.
		mid := (ob.Top + ob.Bottom) / 2
		mitigated := false
		for j := ob.Index + 1; j < i; j++ {
			if candles[j].Low <= mid && candles[j].High >= mid {
				mitigated = true
				break
			}
		}
		if mitigated {
			continue
		}

		// Current candle must touch the OB zone.
		if cur.Low > ob.Top || cur.High < ob.Bottom {
			continue
		}

		bull := ob.Bullish
		entry := mid
		var sl float64
		if bull {
			sl = ob.Bottom - slBuffer
		} else {
			sl = ob.Top + slBuffer
		}
		tp := tpFromRR(entry, sl, bull)
		return buildSignal(
			"OrderBlockMit", bull, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("OB@%d zone=%.2f-%.2f size=%.1f", ob.Index, ob.Bottom, ob.Top, size),
		)
	}
	return Signal{}, false
}
