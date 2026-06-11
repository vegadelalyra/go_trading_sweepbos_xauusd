// Package smc holds the SMC-family entry strategies. Each strategy is a
// struct that pairs its own config (RR, SL buffer, lookbacks, thresholds)
// with a Detect method satisfying internal/strategy.Strategy. Touching one
// strategy's tuning therefore cannot reach into another — the "no
// contamination" guarantee promised by the new architecture.
//
// File layout:
//   common.go    — package-private helpers shared by 2+ strategies
//   register.go  — single init() that registers every strategy in priority order
//   <name>.go    — one file per strategy with its Config + struct + methods
//
// The primitives package (internal/smc) is imported as `prim` to avoid the
// name collision with this package (also `smc`).
package smc

import (
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
)

// findSweep walks backward from atIdx looking for a candle that pierced the
// nearest opposite-direction swing. Returns the wick price that swept.
// Shared by SilverBullet and LiquiditySweepBoS.
func findSweep(candles []core.Candle, swings []prim.Swing, atIdx int, sweepHigh bool) (float64, bool) {
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

// mostRecentCHoChWithin returns the latest CHoCh whose Index is <= atIdx and
// no more than `lookback` candles in the past. Used by SilverBullet.
func mostRecentCHoChWithin(events []prim.StructureEvent, atIdx, lookback int) *prim.StructureEvent {
	for k := len(events) - 1; k >= 0; k-- {
		ev := events[k]
		if ev.Index > atIdx {
			continue
		}
		if atIdx-ev.Index > lookback {
			break
		}
		if ev.Kind == prim.StructCHoChBull || ev.Kind == prim.StructCHoChBear {
			return &events[k]
		}
	}
	return nil
}

// pickRecentFVG returns the freshest in-direction FVG inside [fromIdx, toIdx].
// Used by SilverBullet's "FVG inside the CHoCh-to-now window" gate.
func pickRecentFVG(fvgs []prim.FVG, fromIdx, toIdx int, bull bool) (prim.FVG, bool) {
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
	return prim.FVG{}, false
}

// asianRange returns the high/low of the most recent Asia session preceding
// candle i (NY hours 19:00 prev → 02:00). Requires at least 4 Asia candles
// to consider the session "formed". Used by PowerOfThree, AsianRangeBreakout,
// JudasSwing.
func asianRange(candles []core.Candle, i int) (high, low float64, ok bool) {
	high = -1
	low = 1e18
	count := 0
	for k := i - 1; k >= 0 && i-k < 96; k-- {
		ny := core.ToNY(candles[k].Time)
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

// previousDayLevels returns the prior NY-day high/low. Used by TurtleSoup.
func previousDayLevels(candles []core.Candle, i int) (pdh, pdl float64, ok bool) {
	curDay := core.ToNY(candles[i].Time).Truncate(24 * time.Hour)
	prevDay := curDay.Add(-24 * time.Hour)
	pdh = -1
	pdl = 1e18
	found := false
	for k := i - 1; k >= 0 && i-k < 200; k-- {
		d := core.ToNY(candles[k].Time).Truncate(24 * time.Hour)
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

// inSilverBulletWindow reports whether t falls inside one of the three ICT
// Kill Zones in NY time (London 03:00-04:00, NY-AM 10:00-11:00, NY-PM 14:00-15:00).
func inSilverBulletWindow(t time.Time) bool {
	ny := core.ToNY(t)
	hm := ny.Hour()*60 + ny.Minute()
	if hm >= 3*60 && hm < 4*60 {
		return true
	}
	if hm >= 10*60 && hm < 11*60 {
		return true
	}
	if hm >= 14*60 && hm < 15*60 {
		return true
	}
	return false
}
