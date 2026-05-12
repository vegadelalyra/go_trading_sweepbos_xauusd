package meta

import (
	"math"
	"testing"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
)

// trade is a tiny constructor for the test slices below. We only need the
// fields BayesianShrinkRR actually reads: Strategy, Win, R.
func trade(name string, win bool, r float64) core.Trade {
	return core.Trade{Strategy: name, Win: win, R: r}
}

// Sparse strategies (below MinTradesForShrinkage OR with zero wins) MUST
// collapse to the global prior. This is the "no information" guard — the
// whole point of shrinkage is to NOT trust thin data.
func TestBayesianShrinkRR_SparseStrategiesCollapseToPrior(t *testing.T) {
	trades := []core.Trade{
		// Strategy A: 1 trade — below the floor.
		trade("A", true, 3.0),
		// Strategy B: 10 trades but ZERO wins — no information about
		// asymmetry, even though there's plenty of "history".
		trade("B", false, -1), trade("B", false, -1), trade("B", false, -1),
		trade("B", false, -1), trade("B", false, -1), trade("B", false, -1),
		trade("B", false, -1), trade("B", false, -1), trade("B", false, -1),
		trade("B", false, -1),
		// Strategy C: enough trades + at least one win — should NOT
		// collapse, so we can verify A and B do while C does not.
		trade("C", true, 2.5), trade("C", true, 2.5), trade("C", true, 2.5),
		trade("C", false, -1), trade("C", false, -1),
	}
	got := BayesianShrinkRR(trades, 2.0)
	if math.Abs(got["A"]-2.0) > 1e-9 {
		t.Fatalf("sparse strategy A should collapse to prior 2.0, got %v", got["A"])
	}
	if math.Abs(got["B"]-2.0) > 1e-9 {
		t.Fatalf("no-wins strategy B should collapse to prior 2.0, got %v", got["B"])
	}
	if got["C"] == 2.0 {
		t.Fatalf("strategy C had wins and should shift from prior, got exactly 2.0 (suspicious)")
	}
}

// Shrinkage must be MONOTONIC in n_i: with σ²_intra and σ²_inter held
// roughly constant, a strategy with more trades pulls its θ_shrink
// closer to its own MLE. We construct two strategies with the same
// per-win mean and per-win variance, varying only sample size, and check
// the higher-n one sits closer to its MLE.
func TestBayesianShrinkRR_MoreTradesPullToMLE(t *testing.T) {
	// Two strategies, each MLE = 3.0 with the same intra-variance
	// (alternate 2.5 and 3.5). A has 6 wins, B has 30 wins.
	pattern := []float64{2.5, 3.5}
	build := func(name string, winCount int) []core.Trade {
		out := make([]core.Trade, 0, winCount*2)
		for i := 0; i < winCount; i++ {
			out = append(out, trade(name, true, pattern[i%2]))
		}
		// Add a few losses so nTotal stays > MinTradesForShrinkage.
		out = append(out, trade(name, false, -1), trade(name, false, -1))
		return out
	}
	// Third strategy at a different MLE so σ²_inter > 0.
	third := []core.Trade{
		trade("C", true, 1.5), trade("C", true, 1.5), trade("C", true, 1.5),
		trade("C", true, 1.5), trade("C", true, 1.5), trade("C", true, 1.5),
		trade("C", false, -1),
	}
	all := append(build("A", 6), build("B", 30)...)
	all = append(all, third...)

	got := BayesianShrinkRR(all, 2.0)
	distA := math.Abs(got["A"] - 3.0) // distance from MLE
	distB := math.Abs(got["B"] - 3.0)
	if distB >= distA {
		t.Fatalf("higher-n strategy B should sit CLOSER to its MLE than A: distA=%v distB=%v (shrunk A=%v B=%v)",
			distA, distB, got["A"], got["B"])
	}
}

// When σ²_inter is effectively zero (every strategy has the same MLE), the
// shrinkage formula must NOT divide by zero — every strategy should pull
// to the prior. Regression guard for the degenerate-dispersion case.
func TestBayesianShrinkRR_ZeroInterVariance(t *testing.T) {
	// Three strategies with identical MLE = 2.0 (same as prior). σ²_inter
	// will be 0 to numerical precision.
	mk := func(name string) []core.Trade {
		return []core.Trade{
			trade(name, true, 2.0), trade(name, true, 2.0),
			trade(name, true, 2.0), trade(name, true, 2.0),
			trade(name, true, 2.0), trade(name, false, -1),
		}
	}
	all := append(append(mk("A"), mk("B")...), mk("C")...)
	got := BayesianShrinkRR(all, 2.0)
	for _, name := range []string{"A", "B", "C"} {
		if math.Abs(got[name]-2.0) > 1e-9 {
			t.Fatalf("zero σ²_inter: strategy %s should collapse to prior 2.0, got %v", name, got[name])
		}
	}
}

// Invalid globalRR (<=0) must be sanitised to DefaultGlobalRR so the
// downstream engine override never produces a negative-RR TP. Easy
// regression to introduce by accident.
func TestBayesianShrinkRR_InvalidGlobalFallsBack(t *testing.T) {
	trades := []core.Trade{
		trade("A", true, 1.0), // 1 trade, will collapse to prior anyway
	}
	got := BayesianShrinkRR(trades, 0)
	if got["A"] != DefaultGlobalRR {
		t.Fatalf("globalRR=0 should fall back to DefaultGlobalRR (%v), got %v", DefaultGlobalRR, got["A"])
	}
	got = BayesianShrinkRR(trades, -1)
	if got["A"] != DefaultGlobalRR {
		t.Fatalf("negative globalRR should fall back to DefaultGlobalRR (%v), got %v", DefaultGlobalRR, got["A"])
	}
}

// MedianRRPrior on an empty/no-wins slice falls back to DefaultGlobalRR
// rather than panicking or returning 0 (which would cascade through the
// shrinkage and break TPFromRR's contract).
func TestMedianRRPrior_Degenerate(t *testing.T) {
	if got := MedianRRPrior(nil); got != DefaultGlobalRR {
		t.Fatalf("nil trades → MedianRRPrior should return DefaultGlobalRR, got %v", got)
	}
	losers := []core.Trade{trade("A", false, -1), trade("B", false, -1)}
	if got := MedianRRPrior(losers); got != DefaultGlobalRR {
		t.Fatalf("only-losers → MedianRRPrior should return DefaultGlobalRR, got %v", got)
	}
}

// MedianRRPrior on a clean dataset returns the median of per-strategy
// mean-winning-Rs. Spot-check with three strategies whose means are
// 1.5, 2.0, 3.0 → median 2.0.
func TestMedianRRPrior_Median(t *testing.T) {
	trades := []core.Trade{
		trade("A", true, 1.5),
		trade("B", true, 2.0),
		trade("C", true, 2.5), trade("C", true, 3.5),
	}
	got := MedianRRPrior(trades)
	if math.Abs(got-2.0) > 1e-9 {
		t.Fatalf("median of {1.5, 2.0, 3.0} should be 2.0, got %v", got)
	}
}
