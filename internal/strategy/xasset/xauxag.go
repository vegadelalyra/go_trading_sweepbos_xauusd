// Package xasset holds cross-asset strategies whose alpha source is the
// relationship between two symbols rather than the price action of one.
// XAUXAGPairs is the first member — it reads XAU candles like every other
// strategy but reaches for an aligned XAG series stored on its struct.
package xasset

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// XAUXAGPairsConfig — gold/silver ratio mean-reversion tunables.
type XAUXAGPairsConfig struct {
	RatioOverbought float64 // ratio above this → SHORT XAU
	RatioOversold   float64 // ratio below this → LONG XAU
	SLPercent       float64 // SL as fraction of entry (ratio plays have no structural SL)
	MaxAlignmentHrs float64 // skip if XAG candle is staler than this
	RR              float64
}

func DefaultXAUXAGPairsConfig() XAUXAGPairsConfig {
	return XAUXAGPairsConfig{
		RatioOverbought: 90.0,
		RatioOversold:   45.0,
		SLPercent:       0.02,
		MaxAlignmentHrs: 4.0,
		RR:              strategy.DefaultRR,
	}
}

// XAUXAGPairs — mean-reversion on the gold-to-silver price ratio.
// Single-symbol constraint: only XAU positions are taken, with XAG used
// purely as a signal generator (no pair leg is opened). That asymmetry
// is why the SL is percent-based — a structural SL belongs to the spread,
// not either leg in isolation.
//
// XAG candles are injected via SetXAG before the engine runs. Until then
// the strategy is a no-op (Detect returns false).
type XAUXAGPairs struct {
	Cfg XAUXAGPairsConfig

	mu  sync.RWMutex
	xag []core.Candle
}

func (s *XAUXAGPairs) Meta() strategy.Meta {
	return strategy.Meta{
		Name:        "XAUXAGPairs",
		Family:      strategy.FamilyXAsset,
		Description: "Mean-revert XAU/XAG ratio extremes (XAU leg only)",
	}
}

// SetXAG installs the XAG candle series this strategy will pair against.
// Pass a nil/empty slice to disable the strategy. Safe to call from goroutines
// other than the one running Detect (the engine reads it via RLock).
func (s *XAUXAGPairs) SetXAG(candles []core.Candle) {
	s.mu.Lock()
	s.xag = candles
	s.mu.Unlock()
}

func (s *XAUXAGPairs) Detect(candles []core.Candle, _ prim.Analysis, i int) (core.Signal, bool) {
	if i < 50 {
		return core.Signal{}, false
	}
	s.mu.RLock()
	xagSlice := s.xag
	s.mu.RUnlock()
	if len(xagSlice) == 0 {
		return core.Signal{}, false
	}

	xag, ok := matchingCandle(xagSlice, candles[i].Time, s.Cfg.MaxAlignmentHrs)
	if !ok || xag.Close == 0 {
		return core.Signal{}, false
	}

	ratio := candles[i].Close / xag.Close

	if ratio > s.Cfg.RatioOverbought {
		entry := candles[i].Close
		sl := entry * (1 + s.Cfg.SLPercent)
		tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, false)
		return strategy.BuildSignal(
			"XAUXAGPairs", false, entry, sl, tp, i, candles[i].Time,
			fmt.Sprintf("XAU/XAG ratio=%.1f (>%.0f)", ratio, s.Cfg.RatioOverbought),
		)
	}

	if ratio < s.Cfg.RatioOversold {
		entry := candles[i].Close
		sl := entry * (1 - s.Cfg.SLPercent)
		tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, true)
		return strategy.BuildSignal(
			"XAUXAGPairs", true, entry, sl, tp, i, candles[i].Time,
			fmt.Sprintf("XAU/XAG ratio=%.1f (<%.0f)", ratio, s.Cfg.RatioOversold),
		)
	}

	return core.Signal{}, false
}

// matchingCandle returns the candle whose Time is the largest <= t, provided
// the gap stays within maxHrs. Beyond that we treat the series as having a
// data hole and skip rather than trade on stale silver.
func matchingCandle(series []core.Candle, t time.Time, maxHrs float64) (core.Candle, bool) {
	if len(series) == 0 {
		return core.Candle{}, false
	}
	k := sort.Search(len(series), func(j int) bool {
		return series[j].Time.After(t)
	})
	if k == 0 {
		return core.Candle{}, false
	}
	cand := series[k-1]
	if math.Abs(t.Sub(cand.Time).Hours()) > maxHrs {
		return core.Candle{}, false
	}
	return cand, true
}
