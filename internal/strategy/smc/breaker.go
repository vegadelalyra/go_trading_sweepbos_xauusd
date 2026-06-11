package smc

import (
	"fmt"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// BreakerBlockConfig — tunables for BreakerBlock's retest entry.
type BreakerBlockConfig struct {
	MaxCHoChAge       int     // candles since the CHoCh that broke the OB
	MaxOBToCHoChGap   int     // candles between the OB and the CHoCh that broke it
	RetestTolerancePct float64 // tolerance for "near miss" retests, as a fraction of zone size
	SLBuffer          float64
	RR                float64
}

func DefaultBreakerBlockConfig() BreakerBlockConfig {
	return BreakerBlockConfig{
		MaxCHoChAge:        50,
		MaxOBToCHoChGap:    50,
		RetestTolerancePct: 0.5,
		SLBuffer:           strategy.DefaultSLBuffer,
		RR:                 strategy.DefaultRR,
	}
}

// BreakerBlock — when an OB is invalidated by a CHoCh in the opposite
// direction, the now-broken OB tends to act as supply/demand on retest.
// Enter on retest in the CHoCh direction, SL beyond the OB extreme.
type BreakerBlock struct {
	Cfg BreakerBlockConfig
}

func (s *BreakerBlock) Meta() strategy.Meta {
	return strategy.Meta{
		Name:        "BreakerBlock",
		Family:      strategy.FamilySMC,
		Description: "Retest of a broken Order Block in the CHoCh direction",
	}
}

func (s *BreakerBlock) Detect(candles []core.Candle, a prim.Analysis, i int) (core.Signal, bool) {
	if i < 50 {
		return core.Signal{}, false
	}
	cur := candles[i]

	for k := len(a.Structure) - 1; k >= 0; k-- {
		ev := a.Structure[k]
		if ev.Index >= i {
			continue
		}
		if i-ev.Index > s.Cfg.MaxCHoChAge {
			break
		}
		choCh := ev.Kind == prim.StructCHoChBull || ev.Kind == prim.StructCHoChBear
		if !choCh {
			continue
		}
		isChoChBull := ev.Kind == prim.StructCHoChBull

		var broken *prim.OrderBlock
		for j := len(a.OrderBlocks) - 1; j >= 0; j-- {
			ob := a.OrderBlocks[j]
			if ob.Index >= ev.Index {
				continue
			}
			// OB must have been confirmed before the CHoCh broke it.
			if ob.ResolveIdx >= ev.Index {
				continue
			}
			if ev.Index-ob.Index > s.Cfg.MaxOBToCHoChGap {
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

		zoneSize := broken.Top - broken.Bottom
		tolerance := zoneSize * s.Cfg.RetestTolerancePct
		retest := !(cur.Low > broken.Top || cur.High < broken.Bottom)
		near := cur.Close >= broken.Bottom-tolerance && cur.Close <= broken.Top+tolerance
		if !retest && !near {
			continue
		}

		bull := isChoChBull
		entry := (broken.Top + broken.Bottom) / 2
		var sl float64
		if bull {
			sl = broken.Bottom - s.Cfg.SLBuffer
		} else {
			sl = broken.Top + s.Cfg.SLBuffer
		}
		tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, bull)
		return strategy.BuildSignal(
			"BreakerBlock", bull, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("CHoCh@%d brokenOB@%d", ev.Index, broken.Index),
		)
	}
	return core.Signal{}, false
}
