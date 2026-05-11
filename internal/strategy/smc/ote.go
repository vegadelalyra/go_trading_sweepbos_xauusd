package smc

import (
	"fmt"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// OTEFibonacciConfig — Optimal Trade Entry retracement tunables.
type OTEFibonacciConfig struct {
	MaxBoSAge int
	FibLow    float64 // lower OTE fib (e.g. 0.62)
	FibHigh   float64 // upper OTE fib (e.g. 0.79)
	SLBuffer  float64
	RR        float64
}

func DefaultOTEFibonacciConfig() OTEFibonacciConfig {
	return OTEFibonacciConfig{
		MaxBoSAge: 30,
		FibLow:    0.62,
		FibHigh:   0.79,
		SLBuffer:  strategy.DefaultSLBuffer,
		RR:        strategy.DefaultRR,
	}
}

// OTEFibonacci — after a BoS confirms a new directional leg, enter on the
// 62-79% retracement of that leg (the institutional "premium/discount"
// reload band). SL just beyond the leg start.
type OTEFibonacci struct {
	Cfg OTEFibonacciConfig
}

func (s *OTEFibonacci) Meta() strategy.Meta {
	return strategy.Meta{
		Name:        "OTEFibonacci",
		Family:      strategy.FamilySMC,
		Description: "Entry at the 62-79% retrace of a BoS-confirmed leg",
	}
}

func (s *OTEFibonacci) Detect(candles []core.Candle, a prim.Analysis, i int) (core.Signal, bool) {
	if i < 30 {
		return core.Signal{}, false
	}
	cur := candles[i]

	var bos *prim.StructureEvent
	for k := len(a.Structure) - 1; k >= 0; k-- {
		ev := a.Structure[k]
		if ev.Index >= i {
			continue
		}
		if i-ev.Index > s.Cfg.MaxBoSAge {
			break
		}
		if ev.Kind == prim.StructBOSBull || ev.Kind == prim.StructBOSBear {
			bos = &a.Structure[k]
			break
		}
	}
	if bos == nil {
		return core.Signal{}, false
	}
	bull := bos.Kind == prim.StructBOSBull

	var legEnd float64
	if bull {
		legEnd = candles[bos.Index].High
		for k := bos.Index; k <= i && k < len(candles); k++ {
			if candles[k].High > legEnd {
				legEnd = candles[k].High
			}
		}
	} else {
		legEnd = candles[bos.Index].Low
		for k := bos.Index; k <= i && k < len(candles); k++ {
			if candles[k].Low < legEnd {
				legEnd = candles[k].Low
			}
		}
	}

	legStart := bos.SwingPrice
	legSize := legEnd - legStart
	if (bull && legSize <= 0) || (!bull && legSize >= 0) {
		return core.Signal{}, false
	}

	fibLo := legEnd - legSize*s.Cfg.FibLow
	fibHi := legEnd - legSize*s.Cfg.FibHigh
	oteLo, oteHi := fibLo, fibHi
	if oteLo > oteHi {
		oteLo, oteHi = oteHi, oteLo
	}

	if cur.Low > oteHi || cur.High < oteLo {
		return core.Signal{}, false
	}

	entry := (oteLo + oteHi) / 2
	var sl float64
	if bull {
		sl = legStart - s.Cfg.SLBuffer
	} else {
		sl = legStart + s.Cfg.SLBuffer
	}
	tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, bull)
	return strategy.BuildSignal(
		"OTEFibonacci", bull, entry, sl, tp, i, cur.Time,
		fmt.Sprintf("BoS@%d ote=%.2f-%.2f", bos.Index, oteLo, oteHi),
	)
}
