package smc

import (
	"fmt"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// JudasSwingConfig — fake-out fade tunables.
type JudasSwingConfig struct {
	StartHour int // NY hour where we look for the Judas reversal
	EndHour   int // exclusive
	SLBuffer  float64
	RR        float64
}

func DefaultJudasSwingConfig() JudasSwingConfig {
	return JudasSwingConfig{
		StartHour: 3,
		EndHour:   5,
		SLBuffer:  strategy.DefaultSLBuffer,
		RR:        strategy.DefaultRR,
	}
}

// JudasSwing — at the London open the first move is often a fake-out that
// runs liquidity on ONE side of the Asian range before reversing into the
// real direction. We fade the first move once price reclaims the Asian mid.
type JudasSwing struct {
	Cfg JudasSwingConfig
}

func (s *JudasSwing) Meta() strategy.Meta {
	return strategy.Meta{
		Name:        "JudasSwing",
		Family:      strategy.FamilySMC,
		Description: "Fade the London-open one-sided Asian-range sweep",
	}
}

func (s *JudasSwing) Detect(candles []core.Candle, _ prim.Analysis, i int) (core.Signal, bool) {
	if i < 96 {
		return core.Signal{}, false
	}
	cur := candles[i]
	ny := core.ToNY(cur.Time)
	h := ny.Hour()
	if h < s.Cfg.StartHour || h >= s.Cfg.EndHour {
		return core.Signal{}, false
	}

	asiaHigh, asiaLow, ok := asianRange(candles, i)
	if !ok {
		return core.Signal{}, false
	}
	asiaMid := (asiaHigh + asiaLow) / 2

	londonStart := i
	for k := i - 1; k >= 0; k-- {
		nyk := core.ToNY(candles[k].Time)
		if nyk.Hour() < 2 || nyk.Hour() >= 5 {
			break
		}
		londonStart = k
	}
	if londonStart >= i {
		return core.Signal{}, false
	}

	sweptHigh, sweptLow := false, false
	londonHi, londonLo := candles[londonStart].High, candles[londonStart].Low
	for k := londonStart; k < i; k++ {
		if candles[k].High > asiaHigh {
			sweptHigh = true
		}
		if candles[k].Low < asiaLow {
			sweptLow = true
		}
		if candles[k].High > londonHi {
			londonHi = candles[k].High
		}
		if candles[k].Low < londonLo {
			londonLo = candles[k].Low
		}
	}

	if sweptHigh && !sweptLow && cur.Close < asiaMid {
		entry := cur.Close
		sl := londonHi + s.Cfg.SLBuffer
		tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, false)
		return strategy.BuildSignal(
			"JudasSwing", false, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("AsiaH=%.2f LondonHi=%.2f", asiaHigh, londonHi),
		)
	}
	if sweptLow && !sweptHigh && cur.Close > asiaMid {
		entry := cur.Close
		sl := londonLo - s.Cfg.SLBuffer
		tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, true)
		return strategy.BuildSignal(
			"JudasSwing", true, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("AsiaL=%.2f LondonLo=%.2f", asiaLow, londonLo),
		)
	}
	return core.Signal{}, false
}
