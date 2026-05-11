package smc

import (
	"fmt"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// SilverBulletConfig — Kill-Zone strict ICT trigger config.
type SilverBulletConfig struct {
	MaxCHoChAge int // candles since the CHoCh inside the kill zone
	SLBuffer    float64
	RR          float64
}

func DefaultSilverBulletConfig() SilverBulletConfig {
	return SilverBulletConfig{
		MaxCHoChAge: 6,
		SLBuffer:    strategy.DefaultSLBuffer,
		RR:          strategy.DefaultRR,
	}
}

// SilverBullet — sweep → CHoCh → unfilled FVG, all inside a kill-zone
// hourly window (London 03-04, NY-AM 10-11, NY-PM 14-15). Restrictive on
// purpose; fewer trades but higher quality inside windows where smart money
// historically inverts retail liquidity.
type SilverBullet struct {
	Cfg SilverBulletConfig
}

func (s *SilverBullet) Meta() strategy.Meta {
	return strategy.Meta{
		Name:        "SilverBullet",
		Family:      strategy.FamilySMC,
		Description: "Kill-zone gated sweep + CHoCh + FVG entry",
	}
}

func (s *SilverBullet) Detect(candles []core.Candle, a prim.Analysis, i int) (core.Signal, bool) {
	if i < 20 {
		return core.Signal{}, false
	}
	if !inSilverBulletWindow(candles[i].Time) {
		return core.Signal{}, false
	}

	ch := mostRecentCHoChWithin(a.Structure, i, s.Cfg.MaxCHoChAge)
	if ch == nil {
		return core.Signal{}, false
	}
	bull := ch.Kind == prim.StructCHoChBull

	sweep, swept := findSweep(candles, a.Swings, ch.Index, !bull)
	if !swept {
		return core.Signal{}, false
	}
	fvg, ok := pickRecentFVG(a.FVGs, ch.Index, i, bull)
	if !ok {
		return core.Signal{}, false
	}

	entry := fvg.Mid()
	var sl float64
	if bull {
		sl = sweep - s.Cfg.SLBuffer
	} else {
		sl = sweep + s.Cfg.SLBuffer
	}
	tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, bull)
	return strategy.BuildSignal(
		"SilverBullet", bull, entry, sl, tp, i, candles[i].Time,
		fmt.Sprintf("CHoCh@%d sweep=%.2f fvg=%.2f-%.2f", ch.Index, sweep, fvg.Bottom, fvg.Top),
	)
}
