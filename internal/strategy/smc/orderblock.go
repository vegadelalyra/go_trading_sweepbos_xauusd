package smc

import (
	"fmt"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// OrderBlockMitConfig captures every tunable knob of the OrderBlock-mitigation
// entry. Tweaking a field here has no effect on any other strategy.
type OrderBlockMitConfig struct {
	MinOBSize float64 // dollars; OBs smaller than this are noise on 15m
	SLBuffer  float64 // pad beyond the OB extreme
	RR        float64 // target reward:risk
	MinAge    int     // wait this many candles before touching an OB
	MaxAge    int     // beyond this it's stale
}

// DefaultOrderBlockMitConfig returns the production-tuned defaults derived
// from the run-#2 1873-trade flood post-mortem.
func DefaultOrderBlockMitConfig() OrderBlockMitConfig {
	return OrderBlockMitConfig{
		MinOBSize: 3.0,
		SLBuffer:  strategy.DefaultSLBuffer,
		RR:        strategy.DefaultRR,
		MinAge:    5,
		MaxAge:    50,
	}
}

// OrderBlockMit detects price returning to an unmitigated Order Block produced
// by an earlier BoS. Enter at the OB midpoint, SL just beyond the far edge.
// Requires an inducement (sub-swing sweep between the OB and current price)
// to confirm institutional fuel — without it the touch is more likely a
// continuation than a true mitigation.
type OrderBlockMit struct {
	Cfg OrderBlockMitConfig
}

func (s *OrderBlockMit) Meta() strategy.Meta {
	return strategy.Meta{
		Name:        "OrderBlockMit",
		Family:      strategy.FamilySMC,
		Description: "Mitigation entry at unfilled OB midpoint with inducement gate",
	}
}

func (s *OrderBlockMit) Detect(candles []core.Candle, a prim.Analysis, i int) (core.Signal, bool) {
	if i < 30 {
		return core.Signal{}, false
	}
	cur := candles[i]

	for k := len(a.OrderBlocks) - 1; k >= 0; k-- {
		ob := a.OrderBlocks[k]
		if ob.Index >= i {
			continue
		}
		// OB only "exists" once the confirming structure event has closed.
		if ob.ResolveIdx >= i {
			continue
		}
		age := i - ob.Index
		if age > s.Cfg.MaxAge {
			break
		}
		if age < s.Cfg.MinAge {
			continue
		}

		size := ob.Top - ob.Bottom
		if size < s.Cfg.MinOBSize {
			continue
		}

		// OB midpoint must not have been mitigated already.
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

		if cur.Low > ob.Top || cur.High < ob.Bottom {
			continue
		}

		bull := ob.Bullish

		// Inducement gate.
		if !hasInducement(candles, ob.Index, i, bull) {
			continue
		}

		entry := mid
		var sl float64
		if bull {
			sl = ob.Bottom - s.Cfg.SLBuffer
		} else {
			sl = ob.Top + s.Cfg.SLBuffer
		}
		tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, bull)
		return strategy.BuildSignal(
			"OrderBlockMit", bull, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("OB@%d zone=%.2f-%.2f size=%.1f", ob.Index, ob.Bottom, ob.Top, size),
		)
	}
	return core.Signal{}, false
}

// hasInducement scans (obIdx, i) for any 3-bar local extreme that was later
// pierced within the same window. For bullish setups we want a swept LOW
// (stop run below), for bearish a swept HIGH.
func hasInducement(candles []core.Candle, obIdx, i int, bull bool) bool {
	for m := obIdx + 1; m < i; m++ {
		if m-1 < 0 || m+1 >= len(candles) {
			continue
		}
		if bull && candles[m].Low < candles[m-1].Low && candles[m].Low < candles[m+1].Low {
			for n := m + 1; n < i; n++ {
				if candles[n].Low < candles[m].Low {
					return true
				}
			}
		}
		if !bull && candles[m].High > candles[m-1].High && candles[m].High > candles[m+1].High {
			for n := m + 1; n < i; n++ {
				if candles[n].High > candles[m].High {
					return true
				}
			}
		}
	}
	return false
}
