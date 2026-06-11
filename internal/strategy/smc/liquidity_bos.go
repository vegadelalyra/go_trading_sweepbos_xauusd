package smc

import (
	"fmt"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// LiquiditySweepBoSConfig — tunables for the looser sweep+BoS combo trigger.
type LiquiditySweepBoSConfig struct {
	MaxBoSAge int // candles since the BoS — bigger window than SilverBullet
	SLBuffer  float64
	RR        float64
}

func DefaultLiquiditySweepBoSConfig() LiquiditySweepBoSConfig {
	return LiquiditySweepBoSConfig{
		MaxBoSAge: 6,
		SLBuffer:  strategy.DefaultSLBuffer,
		RR:        strategy.DefaultRR,
	}
}

// LiquiditySweepBoS — the most distilled SMC trigger: liquidity sweep
// followed by BoS, no session / CHoCh requirement. SilverBullet is the
// stricter session-gated subset of this. Entry at market on the current
// close, SL just past the sweep wick.
type LiquiditySweepBoS struct {
	Cfg LiquiditySweepBoSConfig
}

func (s *LiquiditySweepBoS) Meta() strategy.Meta {
	return strategy.Meta{
		Name:        "LiquiditySweepBoS",
		Family:      strategy.FamilySMC,
		Description: "Stop-run + structural break in any session",
	}
}

func (s *LiquiditySweepBoS) Detect(candles []core.Candle, a prim.Analysis, i int) (core.Signal, bool) {
	if i < 30 {
		return core.Signal{}, false
	}
	cur := candles[i]

	for k := len(a.Structure) - 1; k >= 0; k-- {
		ev := a.Structure[k]
		if ev.Index >= i {
			continue
		}
		if i-ev.Index > s.Cfg.MaxBoSAge {
			break
		}
		if ev.Kind != prim.StructBOSBull && ev.Kind != prim.StructBOSBear {
			continue
		}

		bull := ev.Kind == prim.StructBOSBull
		sweep, swept := findSweep(candles, a.Swings, ev.Index, !bull)
		if !swept {
			continue
		}
		entry := cur.Close
		var sl float64
		if bull {
			sl = sweep - s.Cfg.SLBuffer
		} else {
			sl = sweep + s.Cfg.SLBuffer
		}
		tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, bull)
		return strategy.BuildSignal(
			"LiquiditySweepBoS", bull, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("BoS@%d sweep=%.2f", ev.Index, sweep),
		)
	}
	return core.Signal{}, false
}
