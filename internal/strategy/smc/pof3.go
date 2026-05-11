package smc

import (
	"fmt"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// PowerOfThreeConfig — Accumulation-Manipulation-Distribution tunables.
type PowerOfThreeConfig struct {
	ManipLookback int // candles in the London manipulation window
	SLBuffer      float64
	RR            float64
}

func DefaultPowerOfThreeConfig() PowerOfThreeConfig {
	return PowerOfThreeConfig{
		ManipLookback: 6,
		SLBuffer:      strategy.DefaultSLBuffer,
		RR:            strategy.DefaultRR,
	}
}

// PowerOfThree — AMD framing: Asia accumulates, London manipulates by
// sweeping one extreme, NY distributes the real direction. Trigger on the
// manipulation reclaim: a recent London candle broke one side, current
// candle closes back inside.
type PowerOfThree struct {
	Cfg PowerOfThreeConfig
}

func (s *PowerOfThree) Meta() strategy.Meta {
	return strategy.Meta{
		Name:        "PowerOfThree",
		Family:      strategy.FamilySMC,
		Description: "Trade the AMD manipulation reclaim into NY",
	}
}

func (s *PowerOfThree) Detect(candles []core.Candle, _ prim.Analysis, i int) (core.Signal, bool) {
	if i < 96 {
		return core.Signal{}, false
	}
	cur := candles[i]
	ny := core.ToNY(cur.Time)
	h := ny.Hour()
	if h < 2 || h >= 5 {
		return core.Signal{}, false
	}
	asiaHigh, asiaLow, ok := asianRange(candles, i)
	if !ok {
		return core.Signal{}, false
	}

	manipHigh, manipLow := -1.0, 1e18
	for k := i - s.Cfg.ManipLookback; k <= i-1; k++ {
		if k < 0 {
			continue
		}
		if candles[k].High > manipHigh {
			manipHigh = candles[k].High
		}
		if candles[k].Low < manipLow {
			manipLow = candles[k].Low
		}
	}

	if manipHigh > asiaHigh && cur.Close < asiaHigh {
		entry := cur.Close
		sl := manipHigh + s.Cfg.SLBuffer
		tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, false)
		return strategy.BuildSignal(
			"PowerOfThree", false, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("AsiaH=%.2f manip=%.2f", asiaHigh, manipHigh),
		)
	}
	if manipLow < asiaLow && cur.Close > asiaLow {
		entry := cur.Close
		sl := manipLow - s.Cfg.SLBuffer
		tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, true)
		return strategy.BuildSignal(
			"PowerOfThree", true, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("AsiaL=%.2f manip=%.2f", asiaLow, manipLow),
		)
	}
	return core.Signal{}, false
}
