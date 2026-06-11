package ta

import (
	"fmt"
	"math"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// DailyBreakoutConfig — Donchian-style breakout tunables.
type DailyBreakoutConfig struct {
	Lookback     int     // bars defining the range
	ImpulseRatio float64 // body must be at least this fraction of the range
	SLBuffer     float64
	RR           float64
}

func DefaultDailyBreakoutConfig() DailyBreakoutConfig {
	return DailyBreakoutConfig{
		Lookback:     20,
		ImpulseRatio: 0.30,
		SLBuffer:     strategy.DefaultSLBuffer,
		RR:           strategy.DefaultRR,
	}
}

// DailyBreakout — Donchian-style: price closes beyond the N-bar high/low
// after sitting inside it the previous bar, with an impulsive body. SL at
// the opposite edge of the broken range. Captures the start of trending legs;
// the impulse filter screens out limp closes that mostly become traps.
type DailyBreakout struct {
	Cfg DailyBreakoutConfig
}

func (s *DailyBreakout) Meta() strategy.Meta {
	return strategy.Meta{
		Name:        "DailyBreakout",
		Family:      strategy.FamilyTA,
		Description: "Donchian range breakout with impulse-body filter",
	}
}

func (s *DailyBreakout) Detect(candles []core.Candle, _ prim.Analysis, i int) (core.Signal, bool) {
	if i < s.Cfg.Lookback {
		return core.Signal{}, false
	}

	prevHigh := candles[i-s.Cfg.Lookback].High
	prevLow := candles[i-s.Cfg.Lookback].Low
	for k := i - s.Cfg.Lookback + 1; k < i; k++ {
		if candles[k].High > prevHigh {
			prevHigh = candles[k].High
		}
		if candles[k].Low < prevLow {
			prevLow = candles[k].Low
		}
	}

	rangeSize := prevHigh - prevLow
	if rangeSize <= 0 {
		return core.Signal{}, false
	}

	cur := candles[i]
	bodySize := math.Abs(cur.Close - cur.Open)
	if bodySize < rangeSize*s.Cfg.ImpulseRatio {
		return core.Signal{}, false
	}

	bullBO := cur.Close > prevHigh && candles[i-1].Close <= prevHigh
	bearBO := cur.Close < prevLow && candles[i-1].Close >= prevLow
	if !bullBO && !bearBO {
		return core.Signal{}, false
	}

	bull := bullBO
	entry := cur.Close
	var sl float64
	var reason string
	if bull {
		sl = prevLow - s.Cfg.SLBuffer
		if sl >= entry {
			return core.Signal{}, false
		}
		reason = fmt.Sprintf("%d-bar BO above %.2f", s.Cfg.Lookback, prevHigh)
	} else {
		sl = prevHigh + s.Cfg.SLBuffer
		if sl <= entry {
			return core.Signal{}, false
		}
		reason = fmt.Sprintf("%d-bar BO below %.2f", s.Cfg.Lookback, prevLow)
	}
	tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, bull)

	return strategy.BuildSignal("DailyBreakout", bull, entry, sl, tp, i, cur.Time, reason)
}
