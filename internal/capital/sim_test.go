package capital

import (
	"math"
	"testing"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
)

// makeTrades builds a synthetic stream where each trade lands on its
// own UTC day so the day-rollover bookkeeping never interferes with
// the autocorr math we're testing. Win is derived from R>=0.
func makeTrades(rs []float64) []core.Trade {
	out := make([]core.Trade, len(rs))
	base := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	for i, r := range rs {
		entry := base.AddDate(0, 0, i)
		out[i] = core.Trade{
			Strategy:  "synthetic",
			EntryTime: entry,
			ExitTime:  entry.Add(time.Hour),
			R:         r,
			Win:       r >= 0,
		}
	}
	return out
}

// baseCfg is a no-martingale, no-reload setup that keeps the simulator's
// behavior purely a function of first-trade-per-day risk. With one trade
// per UTC day we step through the stream deterministically.
func baseCfg() Config {
	return Config{
		StartCapital:      1000,
		FirstRiskPct:      0.10,
		MartingaleRiskPct: 0,
		MaxTradesPerDay:   1,
		ReloadThreshold:   0, // never reload — we want raw capital drift
		ReloadAmount:      0,
	}
}

func TestAutocorr_DisabledLeavesRiskUntouched(t *testing.T) {
	// Stream with high positive autocorr — were the penalty active it
	// would bite. With AutocorrEnabled=false, the run must be bit-exact
	// to the legacy path.
	rs := []float64{-1, -1, -1, -1, -1, 2, 2, 2, 2, 2, -1, -1, -1, -1, -1, 2, 2, 2, 2, 2, -1, -1, 2, 2}
	trades := makeTrades(rs)

	cfg := baseCfg()
	legacy := Simulate("legacy", trades, cfg)

	cfg2 := baseCfg()
	cfg2.AutocorrEnabled = false
	cfg2.AutocorrWindow = 20
	cfg2.AutocorrLambda = 1.5
	off := Simulate("off", trades, cfg2)

	if legacy.FinalCapital != off.FinalCapital {
		t.Fatalf("AutocorrEnabled=false changed FinalCapital: legacy=%.6f off=%.6f", legacy.FinalCapital, off.FinalCapital)
	}
	if legacy.MaxDrawdownPct != off.MaxDrawdownPct {
		t.Fatalf("AutocorrEnabled=false changed MaxDrawdownPct: legacy=%.6f off=%.6f", legacy.MaxDrawdownPct, off.MaxDrawdownPct)
	}
}

func TestAutocorr_PositiveCorrelationReducesRisk(t *testing.T) {
	// Block-clustered stream: 5 losses, 5 wins, repeated. This produces
	// strongly positive ρ_1 because each R is highly likely to share its
	// sign with the next. The penalty must shrink size on the wins that
	// follow the cluster fill, so net upside should be lower than the
	// legacy run.
	rs := []float64{}
	for c := 0; c < 6; c++ {
		for i := 0; i < 5; i++ {
			rs = append(rs, -1)
		}
		for i := 0; i < 5; i++ {
			rs = append(rs, 2)
		}
	}
	trades := makeTrades(rs)

	legacy := Simulate("legacy", trades, baseCfg())

	cfg := baseCfg()
	cfg.AutocorrEnabled = true
	cfg.AutocorrWindow = 20
	cfg.AutocorrLambda = 1.5
	on := Simulate("on", trades, cfg)

	if !(on.FinalCapital < legacy.FinalCapital) {
		t.Fatalf("expected autocorr-on FinalCapital < legacy; got on=%.4f legacy=%.4f", on.FinalCapital, legacy.FinalCapital)
	}
	// Sanity: lag-1 autocorr of the full R stream is clearly positive.
	if rho := lag1Autocorr(rs); rho <= 0 {
		t.Fatalf("expected positive ρ_1 on block-clustered stream, got %.4f", rho)
	}
}

func TestAutocorr_NegativeCorrelationDoesNotReward(t *testing.T) {
	// Alternating wins/losses → strongly NEGATIVE ρ_1. The max(0, ρ_1)
	// clamp must keep the penalty at zero, so the on/off curves match.
	rs := []float64{}
	for i := 0; i < 30; i++ {
		if i%2 == 0 {
			rs = append(rs, -1)
		} else {
			rs = append(rs, 2)
		}
	}
	if rho := lag1Autocorr(rs); rho >= 0 {
		t.Fatalf("expected negative ρ_1 on alternating stream, got %.4f", rho)
	}
	trades := makeTrades(rs)

	legacy := Simulate("legacy", trades, baseCfg())

	cfg := baseCfg()
	cfg.AutocorrEnabled = true
	cfg.AutocorrWindow = 20
	cfg.AutocorrLambda = 1.5
	on := Simulate("on", trades, cfg)

	if math.Abs(on.FinalCapital-legacy.FinalCapital) > 1e-9 {
		t.Fatalf("negative ρ_1 must not change FinalCapital: legacy=%.6f on=%.6f", legacy.FinalCapital, on.FinalCapital)
	}
}

func TestAutocorr_BelowWindowSize_NoEffect(t *testing.T) {
	// Only 10 trades but AutocorrWindow=20. The penalty must stay off
	// because the rolling buffer never fills — output must equal legacy.
	rs := []float64{-1, -1, -1, -1, -1, 2, 2, 2, 2, 2}
	trades := makeTrades(rs)

	legacy := Simulate("legacy", trades, baseCfg())

	cfg := baseCfg()
	cfg.AutocorrEnabled = true
	cfg.AutocorrWindow = 20
	cfg.AutocorrLambda = 1.5
	on := Simulate("on", trades, cfg)

	if math.Abs(on.FinalCapital-legacy.FinalCapital) > 1e-9 {
		t.Fatalf("below-window stream must not change FinalCapital: legacy=%.6f on=%.6f", legacy.FinalCapital, on.FinalCapital)
	}
}

func TestLag1Autocorr_KnownValues(t *testing.T) {
	// Constant stream → variance=0 → ρ_1 must be 0 (degenerate guard).
	if got := lag1Autocorr([]float64{1, 1, 1, 1, 1}); got != 0 {
		t.Fatalf("constant stream: want 0, got %v", got)
	}
	// Empty / single-element → 0.
	if got := lag1Autocorr(nil); got != 0 {
		t.Fatalf("nil: want 0, got %v", got)
	}
	if got := lag1Autocorr([]float64{1}); got != 0 {
		t.Fatalf("len=1: want 0, got %v", got)
	}
	// Strictly alternating ±1: ρ_1 = -1 + O(1/n). For n=10 the
	// population formula gives exactly -9/10 = -0.9 (covariance has 9
	// terms of -1, variance is 1; ÷n cancels except for the missing
	// last cov term).
	alt := []float64{1, -1, 1, -1, 1, -1, 1, -1, 1, -1}
	got := lag1Autocorr(alt)
	if math.Abs(got-(-0.9)) > 1e-12 {
		t.Fatalf("alternating len=10: want -0.9, got %v", got)
	}
}
