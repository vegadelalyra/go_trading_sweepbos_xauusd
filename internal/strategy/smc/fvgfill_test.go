package smc

import (
	"testing"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
)

// makeFlatCandles builds N flat candles around `base`, then carves a
// retest window at `retestIdx` whose Low/High straddles `mid` so the
// "current candle touches the FVG midpoint" gate in Detect fires.
func makeFlatCandles(n int, base, mid float64, retestIdx int) []core.Candle {
	out := make([]core.Candle, n)
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range out {
		out[i] = core.Candle{
			Time:  start.Add(time.Duration(i) * 15 * time.Minute),
			Open:  base, High: base + 0.01, Low: base - 0.01, Close: base,
		}
	}
	if retestIdx >= 0 && retestIdx < n {
		c := &out[retestIdx]
		c.Low = mid - 0.5
		c.High = mid + 0.5
		c.Open = mid
		c.Close = mid
	}
	return out
}

// buildPopulation creates a synthetic FVG population: `n` "normal" FVGs of
// `normalSize` dollars plus an optional `bigSize` outlier appended at the
// end. All FVGs are bullish with Index spaced one bar apart so Detect's
// "f.Index < i" filter accepts them all when called at index n+2.
func buildPopulation(normalSize float64, n int, bigSize float64, includeBig bool) ([]prim.FVG, int) {
	out := make([]prim.FVG, 0, n+1)
	bottom := 2000.0
	for k := 0; k < n; k++ {
		out = append(out, prim.FVG{
			Index:   k + 1,
			Bottom:  bottom,
			Top:     bottom + normalSize,
			Bullish: true,
		})
	}
	// Target FVG is the last one — what Detect will iterate to first.
	if includeBig {
		out = append(out, prim.FVG{
			Index:   n + 1,
			Bottom:  bottom,
			Top:     bottom + bigSize,
			Bullish: true,
		})
	} else {
		out = append(out, prim.FVG{
			Index:   n + 1,
			Bottom:  bottom,
			Top:     bottom + normalSize,
			Bullish: true,
		})
	}
	// Detect call index — comfortably past MinAge, well under MaxAge.
	atIdx := n + 5
	return out, atIdx
}

// TestFVGFill_VarianceExclusionRejectsNewsGap — a 5σ-outsized FVG must be
// dropped when the variance gate is on.
func TestFVGFill_VarianceExclusionRejectsNewsGap(t *testing.T) {
	cfg := DefaultFVGFillConfig()
	cfg.MaxAge = 100
	s := &FVGFill{Cfg: cfg}

	fvgs, atIdx := buildPopulation(3.0, 40, 50.0, true)
	target := fvgs[len(fvgs)-1]
	candles := makeFlatCandles(atIdx+1, 2010.0, target.Mid(), atIdx)
	a := prim.Analysis{FVGs: fvgs}

	if _, ok := s.Detect(candles, a, atIdx); ok {
		t.Fatalf("expected variance-exclusion to reject a 5σ FVG (size=50, mean~3), got signal")
	}
}

// TestFVGFill_VarianceExclusionAcceptsNormalFVG — a within-distribution FVG
// must still produce a signal when the variance gate is on.
func TestFVGFill_VarianceExclusionAcceptsNormalFVG(t *testing.T) {
	cfg := DefaultFVGFillConfig()
	cfg.MaxAge = 100
	s := &FVGFill{Cfg: cfg}

	fvgs, atIdx := buildPopulation(3.0, 40, 0, false)
	target := fvgs[len(fvgs)-1]
	candles := makeFlatCandles(atIdx+1, 2010.0, target.Mid(), atIdx)
	a := prim.Analysis{FVGs: fvgs}

	sig, ok := s.Detect(candles, a, atIdx)
	if !ok {
		t.Fatalf("expected normal-sized FVG (size=3, mean=3) to pass variance gate")
	}
	if sig.Strategy != "FVGFill" {
		t.Fatalf("got strategy=%q, want FVGFill", sig.Strategy)
	}
}

// TestFVGFill_VarianceExclusionDisabled — with the gate off the same 5σ
// outlier passes (legacy behaviour preserved for backtest reproducibility).
func TestFVGFill_VarianceExclusionDisabled(t *testing.T) {
	cfg := DefaultFVGFillConfig()
	cfg.MaxAge = 100
	cfg.VarianceExclusionEnabled = false
	s := &FVGFill{Cfg: cfg}

	fvgs, atIdx := buildPopulation(3.0, 40, 50.0, true)
	target := fvgs[len(fvgs)-1]
	candles := makeFlatCandles(atIdx+1, 2010.0, target.Mid(), atIdx)
	a := prim.Analysis{FVGs: fvgs}

	if _, ok := s.Detect(candles, a, atIdx); !ok {
		t.Fatalf("expected disabled variance gate to let the 5σ FVG through, got no signal")
	}
}
