// Package ta holds classical TA strategies — non-SMC trend / mean-reversion
// / breakout setups. Each strategy keeps its own Config so EMA periods, RSI
// thresholds, lookbacks and band widths can drift independently of any
// other strategy in the system.
package ta

import (
	"fmt"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/indicator"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// EMACrossConfig — tunables for the EMA-cross trend entry.
type EMACrossConfig struct {
	FastPeriod     int
	SlowPeriod     int
	RSIPeriod      int
	RSILongMin     float64 // RSI floor for longs
	RSIShortMax    float64 // RSI ceiling for shorts
	SLLookback     int     // bars to scan for swing-low/high SL anchor
	SLBuffer       float64
	RR             float64
	MinWarmupBars  int // safety floor — refuse before SlowPeriod + extras
}

func DefaultEMACrossConfig() EMACrossConfig {
	return EMACrossConfig{
		FastPeriod:    50,
		SlowPeriod:    200,
		RSIPeriod:     14,
		RSILongMin:    55.0,
		RSIShortMax:   45.0,
		SLLookback:    10,
		SLBuffer:      strategy.DefaultSLBuffer,
		RR:            strategy.DefaultRR,
		MinWarmupBars: 205,
	}
}

// EMACross — fast/slow EMA cross confirmed by RSI on the same side. SL
// parks just beyond the recent swing (configurable lookback) so we exit
// if the trend thesis is invalidated.
type EMACross struct {
	Cfg EMACrossConfig
}

func (s *EMACross) Meta() strategy.Meta {
	return strategy.Meta{
		Name:        "EMACross",
		Family:      strategy.FamilyTA,
		Description: "EMA fast/slow cross + RSI confirmation entry",
	}
}

func (s *EMACross) Detect(candles []core.Candle, _ prim.Analysis, i int) (core.Signal, bool) {
	if i < s.Cfg.MinWarmupBars {
		return core.Signal{}, false
	}

	closes := make([]float64, i+1)
	for k := 0; k <= i; k++ {
		closes[k] = candles[k].Close
	}

	emaFast := indicator.EMA(closes, s.Cfg.FastPeriod)
	emaSlow := indicator.EMA(closes, s.Cfg.SlowPeriod)
	rsi := indicator.RSI(closes, s.Cfg.RSIPeriod)

	bullCross := emaFast[i] > emaSlow[i] && emaFast[i-1] <= emaSlow[i-1]
	bearCross := emaFast[i] < emaSlow[i] && emaFast[i-1] >= emaSlow[i-1]
	if !bullCross && !bearCross {
		return core.Signal{}, false
	}

	bull := bullCross
	if bull && rsi[i] < s.Cfg.RSILongMin {
		return core.Signal{}, false
	}
	if !bull && rsi[i] > s.Cfg.RSIShortMax {
		return core.Signal{}, false
	}

	entry := candles[i].Close
	var sl float64
	if bull {
		lo := candles[i].Low
		for k := i - s.Cfg.SLLookback; k <= i; k++ {
			if k < 0 {
				continue
			}
			if candles[k].Low < lo {
				lo = candles[k].Low
			}
		}
		sl = lo - s.Cfg.SLBuffer
	} else {
		hi := candles[i].High
		for k := i - s.Cfg.SLLookback; k <= i; k++ {
			if k < 0 {
				continue
			}
			if candles[k].High > hi {
				hi = candles[k].High
			}
		}
		sl = hi + s.Cfg.SLBuffer
	}
	tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, bull)

	reason := fmt.Sprintf("EMA%d/%d cross + RSI=%.1f", s.Cfg.FastPeriod, s.Cfg.SlowPeriod, rsi[i])
	return strategy.BuildSignal("EMACross", bull, entry, sl, tp, i, candles[i].Time, reason)
}
