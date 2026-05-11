package smc

import (
	"fmt"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// FVGFillConfig — FVG mid-fill tunables.
type FVGFillConfig struct {
	MinFVGSize float64 // dollars; below this the FVG is noise
	MinAge     int     // candles since FVG before we trade it
	MaxAge     int     // age beyond which the FVG is stale
	SLBuffer   float64
	RR         float64
}

func DefaultFVGFillConfig() FVGFillConfig {
	return FVGFillConfig{
		MinFVGSize: 2.0,
		MinAge:     3,
		MaxAge:     30,
		SLBuffer:   strategy.DefaultSLBuffer,
		RR:         strategy.DefaultRR,
	}
}

// FVGFill — price returns to a recent Fair Value Gap; enter at the midpoint,
// SL beyond the far edge. Quality gates: skip micro FVGs and already-
// mitigated ones; first match wins.
type FVGFill struct {
	Cfg FVGFillConfig
}

func (s *FVGFill) Meta() strategy.Meta {
	return strategy.Meta{
		Name:        "FVGFill",
		Family:      strategy.FamilySMC,
		Description: "Fair Value Gap mid-fill, unmitigated only",
	}
}

func (s *FVGFill) Detect(candles []core.Candle, a prim.Analysis, i int) (core.Signal, bool) {
	if i < 30 {
		return core.Signal{}, false
	}
	cur := candles[i]

	for k := len(a.FVGs) - 1; k >= 0; k-- {
		f := a.FVGs[k]
		if f.Index >= i {
			continue
		}
		age := i - f.Index
		if age > s.Cfg.MaxAge {
			break
		}
		if age < s.Cfg.MinAge {
			continue
		}

		size := f.Top - f.Bottom
		if size < s.Cfg.MinFVGSize {
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
			sl = f.Bottom - s.Cfg.SLBuffer
		} else {
			sl = f.Top + s.Cfg.SLBuffer
		}
		tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, bull)
		return strategy.BuildSignal(
			"FVGFill", bull, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("FVG@%d zone=%.2f-%.2f size=%.1f", f.Index, f.Bottom, f.Top, size),
		)
	}
	return core.Signal{}, false
}
