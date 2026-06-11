package smc

import (
	"fmt"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// TurtleSoupConfig — PDH/PDL liquidity-fade tunables.
type TurtleSoupConfig struct {
	SLBuffer float64
	RR       float64
}

func DefaultTurtleSoupConfig() TurtleSoupConfig {
	return TurtleSoupConfig{
		SLBuffer: strategy.DefaultSLBuffer,
		RR:       strategy.DefaultRR,
	}
}

// TurtleSoup — classical liquidity fade against the previous-day high/low.
// Prev candle's wick swept the level, current candle closes back inside the
// prior day's range. Retail breakout traders just got trapped — we fade
// them. SL just beyond the sweep wick.
//
// MaxSLPoints / MaxHoldCandles are enforced at the engine boundary; this
// strategy only describes the setup physics.
type TurtleSoup struct {
	Cfg TurtleSoupConfig
}

func (s *TurtleSoup) Meta() strategy.Meta {
	return strategy.Meta{
		Name:        "TurtleSoup",
		Family:      strategy.FamilySMC,
		Description: "Fade the wick that swept previous-day high/low",
	}
}

func (s *TurtleSoup) Detect(candles []core.Candle, _ prim.Analysis, i int) (core.Signal, bool) {
	if i < 96 {
		return core.Signal{}, false
	}
	pdh, pdl, ok := previousDayLevels(candles, i)
	if !ok {
		return core.Signal{}, false
	}
	cur := candles[i]
	prev := candles[i-1]

	if prev.High > pdh && cur.Close < pdh {
		entry := cur.Close
		sl := prev.High + s.Cfg.SLBuffer
		tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, false)
		return strategy.BuildSignal(
			"TurtleSoup", false, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("PDH=%.2f sweep=%.2f reclaim", pdh, prev.High),
		)
	}
	if prev.Low < pdl && cur.Close > pdl {
		entry := cur.Close
		sl := prev.Low - s.Cfg.SLBuffer
		tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, true)
		return strategy.BuildSignal(
			"TurtleSoup", true, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("PDL=%.2f sweep=%.2f reclaim", pdl, prev.Low),
		)
	}
	return core.Signal{}, false
}
