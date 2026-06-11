package smc

import (
	"fmt"
	"math"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// AsianRangeBreakoutConfig — genuine breakout tunables.
type AsianRangeBreakoutConfig struct {
	StartHour      int     // NY hour where the breakout window opens
	EndHour        int     // exclusive
	MinBodyVsRange float64 // body must be at least this fraction of the Asian range
	SLBuffer       float64
	RR             float64
}

func DefaultAsianRangeBreakoutConfig() AsianRangeBreakoutConfig {
	return AsianRangeBreakoutConfig{
		StartHour:      2,
		EndHour:        9,
		MinBodyVsRange: 0.15,
		SLBuffer:       strategy.DefaultSLBuffer,
		RR:             strategy.DefaultRR,
	}
}

// AsianRangeBreakout — opposite hypothesis to PowerOfThree: occasionally
// the London open is a real breakout of the Asian range, not a manipulation.
// Trade in the direction of an impulsive candle that closes beyond, only in
// the first London-through-NY-open hours.
type AsianRangeBreakout struct {
	Cfg AsianRangeBreakoutConfig
}

func (s *AsianRangeBreakout) Meta() strategy.Meta {
	return strategy.Meta{
		Name:        "AsianRangeBO",
		Family:      strategy.FamilySMC,
		Description: "Trade impulsive breakout of the Asian range during London",
	}
}

func (s *AsianRangeBreakout) Detect(candles []core.Candle, _ prim.Analysis, i int) (core.Signal, bool) {
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
	asiaSize := asiaHigh - asiaLow
	if asiaSize <= 0 {
		return core.Signal{}, false
	}

	bodySize := math.Abs(cur.Close - cur.Open)
	if bodySize < asiaSize*s.Cfg.MinBodyVsRange {
		return core.Signal{}, false
	}

	prev := candles[i-1]

	if cur.Close > asiaHigh && prev.Close <= asiaHigh {
		entry := cur.Close
		sl := prev.Low - s.Cfg.SLBuffer
		if sl >= entry {
			return core.Signal{}, false
		}
		tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, true)
		return strategy.BuildSignal(
			"AsianRangeBO", true, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("AsiaH=%.2f break=%.2f body=%.2f", asiaHigh, cur.Close, bodySize),
		)
	}
	if cur.Close < asiaLow && prev.Close >= asiaLow {
		entry := cur.Close
		sl := prev.High + s.Cfg.SLBuffer
		if sl <= entry {
			return core.Signal{}, false
		}
		tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, false)
		return strategy.BuildSignal(
			"AsianRangeBO", false, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("AsiaL=%.2f break=%.2f body=%.2f", asiaLow, cur.Close, bodySize),
		)
	}
	return core.Signal{}, false
}
