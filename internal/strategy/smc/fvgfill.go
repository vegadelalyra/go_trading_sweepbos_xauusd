package smc

import (
	"fmt"
	"math"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/indicator"
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

	// VarianceExclusionEnabled rejects oversized FVGs that almost always
	// come from news gaps / shock events — the imbalance is real but the
	// retest behaviour is unstable. Default ON.
	VarianceExclusionEnabled bool
	// VarianceSigmaMult is the population-sigma cutoff (mean + k·σ).
	VarianceSigmaMult float64
	// VarianceATRMult is the ATR(14) cutoff (k·ATR). The two criteria are
	// OR'd: rejection requires size > max(mean+kσ·σ, kATR·ATR14).
	VarianceATRMult float64
	// VarianceMinSamples is the minimum FVG population size before the
	// sigma criterion is trusted. Below this we fall back to ATR-only.
	VarianceMinSamples int
}

func DefaultFVGFillConfig() FVGFillConfig {
	return FVGFillConfig{
		MinFVGSize:               2.0,
		MinAge:                   3,
		MaxAge:                   30,
		SLBuffer:                 strategy.DefaultSLBuffer,
		RR:                       strategy.DefaultRR,
		VarianceExclusionEnabled: true,
		VarianceSigmaMult:        3.0,
		VarianceATRMult:          3.0,
		VarianceMinSamples:       30,
	}
}

// FVGFill — price returns to a recent Fair Value Gap; enter at the midpoint,
// SL beyond the far edge. Quality gates: skip micro FVGs, already-mitigated
// ones, and news-gap outliers (variance exclusion); first match wins.
//
// Consequent Encroachment 50%: entry sits at f.Mid() by construction — the
// CE midpoint is the canonical SMC fill price, no extra gate required.
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

	// Variance exclusion: precompute mean/stddev across FVGs known at i
	// (i.e. f.Index < i) whose size already clears MinFVGSize — micro
	// FVGs are excluded from the population so noise doesn't drag the
	// mean down. Single pass; len(a.FVGs) is small per candle.
	var sigmaCut, atrCut float64
	varianceOn := s.Cfg.VarianceExclusionEnabled
	if varianceOn {
		var n int
		var sum, sumSq float64
		for k := 0; k < len(a.FVGs); k++ {
			f := a.FVGs[k]
			if f.Index >= i {
				break
			}
			sz := f.Top - f.Bottom
			if sz < s.Cfg.MinFVGSize {
				continue
			}
			n++
			sum += sz
			sumSq += sz * sz
		}
		atr14 := indicator.ATRAt(candles, i, 14)
		atrCut = s.Cfg.VarianceATRMult * atr14
		if n >= s.Cfg.VarianceMinSamples {
			mean := sum / float64(n)
			variance := sumSq/float64(n) - mean*mean
			if variance < 0 {
				variance = 0
			}
			stddev := math.Sqrt(variance)
			sigmaCut = mean + s.Cfg.VarianceSigmaMult*stddev
		}
		// If n < MinSamples the sigma criterion is unreliable — atrCut
		// alone gates the rejection (single-criterion fallback).
	}

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

		if varianceOn {
			cutoff := atrCut
			if sigmaCut > cutoff {
				cutoff = sigmaCut
			}
			if cutoff > 0 && size > cutoff {
				continue
			}
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
