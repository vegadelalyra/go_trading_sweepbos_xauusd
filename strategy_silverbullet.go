package main

import (
	"fmt"
	"time"
)

// SilverBullet (ICT) ────────────────────────────────────────────────────────
//
// Setup, strictly inside one of three "Kill Zone" hourly windows in NY time:
//   • London — 03:00–04:00
//   • NY AM   — 10:00–11:00
//   • NY PM   — 14:00–15:00
//
// Sequence required, in order:
//   1. A liquidity sweep of the most recent opposite swing (high for shorts,
//      low for longs) occurred within the previous ~6 candles.
//   2. A Change-of-Character (CHoCh) in the new direction closed within the
//      previous ~6 candles.
//   3. There is an unfilled FVG, in the CHoCh direction, between the CHoCh
//      candle and now.
//
// Entry: midpoint of that FVG (a limit-style fill).
// SL:    just beyond the sweep extreme.
// TP:    1:2 RR.
//
// The strategy is intentionally restrictive — it produces fewer but
// higher-quality trades inside the windows where smart money historically
// reverses retail liquidity.

func SilverBullet(candles []Candle, a Analysis, i int) (Signal, bool) {
	if i < 20 {
		return Signal{}, false
	}
	if !inSilverBulletWindow(candles[i].Time) {
		return Signal{}, false
	}

	ch := mostRecentCHoChWithin(a.Structure, i, 6)
	if ch == nil {
		return Signal{}, false
	}
	bull := ch.Kind == StructCHoChBull

	sweep, swept := findSweep(candles, a.Swings, ch.Index, !bull)
	if !swept {
		return Signal{}, false
	}
	fvg, ok := pickRecentFVG(a.FVGs, ch.Index, i, bull)
	if !ok {
		return Signal{}, false
	}

	entry := fvg.Mid()
	var sl float64
	if bull {
		sl = sweep - slBuffer
	} else {
		sl = sweep + slBuffer
	}
	tp := tpFromRR(entry, sl, bull)

	return buildSignal(
		"SilverBullet", bull, entry, sl, tp, i, candles[i].Time,
		fmt.Sprintf("CHoCh@%d sweep=%.2f fvg=%.2f-%.2f", ch.Index, sweep, fvg.Bottom, fvg.Top),
	)
}

// ── Strategy-local helpers ──────────────────────────────────────────────────

func inSilverBulletWindow(t time.Time) bool {
	ny := toNY(t)
	hm := ny.Hour()*60 + ny.Minute()
	// London kill zone
	if hm >= 3*60 && hm < 4*60 {
		return true
	}
	// NY AM kill zone
	if hm >= 10*60 && hm < 11*60 {
		return true
	}
	// NY PM kill zone
	if hm >= 14*60 && hm < 15*60 {
		return true
	}
	return false
}

func mostRecentCHoChWithin(events []StructureEvent, atIdx, lookback int) *StructureEvent {
	for k := len(events) - 1; k >= 0; k-- {
		ev := events[k]
		if ev.Index > atIdx {
			continue
		}
		if atIdx-ev.Index > lookback {
			break
		}
		if ev.Kind == StructCHoChBull || ev.Kind == StructCHoChBear {
			return &events[k]
		}
	}
	return nil
}

// findSweep walks backward from atIdx looking for a candle that pierced the
// nearest opposite-direction swing. Returns the wick price that swept.
func findSweep(candles []Candle, swings []Swing, atIdx int, sweepHigh bool) (float64, bool) {
	for k := atIdx - 1; k >= 0 && atIdx-k <= 8; k-- {
		c := candles[k]
		for s := len(swings) - 1; s >= 0; s-- {
			sw := swings[s]
			if sw.Index >= k {
				continue
			}
			if k-sw.Index > 20 {
				break
			}
			if sweepHigh && sw.IsHigh && c.High > sw.Price {
				return c.High, true
			}
			if !sweepHigh && !sw.IsHigh && c.Low < sw.Price {
				return c.Low, true
			}
		}
	}
	return 0, false
}

// pickRecentFVG returns the freshest in-direction FVG inside the window
// [fromIdx, toIdx]. Strategies that only want UNfilled FVGs should track
// the Filled flag themselves.
func pickRecentFVG(fvgs []FVG, fromIdx, toIdx int, bull bool) (FVG, bool) {
	for k := len(fvgs) - 1; k >= 0; k-- {
		f := fvgs[k]
		if f.Index > toIdx || f.Index < fromIdx {
			continue
		}
		if f.Bullish != bull {
			continue
		}
		return f, true
	}
	return FVG{}, false
}
