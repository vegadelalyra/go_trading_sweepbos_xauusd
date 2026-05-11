package meta

import (
	"math"
	"testing"
)

// SharpeWeight is the single source of truth shared by LoadWeights (SQL
// aggregation) and wfa.computeWeights (in-memory). Any drift in this
// function silently biases WFA OOS results, so these guard the edges
// every caller relies on.
func TestSharpeWeight_EdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		expectancy float64
		stddev     float64
		trades     int
		want       float64
	}{
		{"zero stddev", 0.5, 0, 100, 0},
		{"tiny stddev below epsilon", 0.5, 1e-10, 100, 0},
		{"negative expectancy", -0.1, 0.5, 100, 0},
		{"zero expectancy", 0, 0.5, 100, 0},
		{"zero trades", 0.5, 0.5, 0, 0},
		{"negative trades", 0.5, 0.5, -1, 0},
		// Positive case: weight = (exp/σ) × log(N+1).
		// exp=0.5, σ=0.5, N=99 → 1.0 × log(100) ≈ 4.605.
		{"normal positive", 0.5, 0.5, 99, math.Log(100)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SharpeWeight(tc.expectancy, tc.stddev, tc.trades)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("SharpeWeight(%v,%v,%d) = %v, want %v",
					tc.expectancy, tc.stddev, tc.trades, got, tc.want)
			}
		})
	}
}

// Variance-blindness regression: two strategies with the same expectancy
// but different stddev MUST receive different weights. This is the whole
// reason SharpeWeight replaced the legacy max(0, exp)·sqrt(N) formula —
// the legacy version returned the same weight for both inputs below.
func TestSharpeWeight_VarianceAware(t *testing.T) {
	steady := SharpeWeight(0.5, 0.2, 100)
	choppy := SharpeWeight(0.5, 2.0, 100)
	if steady <= choppy {
		t.Fatalf("expected lower-stddev strategy to dominate: steady=%v choppy=%v", steady, choppy)
	}
	ratio := steady / choppy
	if ratio < 9 || ratio > 11 {
		t.Fatalf("steady/choppy ratio = %v, expected ≈10 (σ ratio inverted)", ratio)
	}
}

// log(N+1) compression: 1000 trades should NOT outweigh 1 trade by 30×
// (which the legacy sqrt(N) gave). The fix dampens the bonus to ~7×.
func TestSharpeWeight_LogCompression(t *testing.T) {
	one := SharpeWeight(0.5, 0.5, 1)
	thousand := SharpeWeight(0.5, 0.5, 1000)
	ratio := thousand / one
	if ratio > 10 {
		t.Fatalf("compression failed: 1000/1 ratio = %v, expected ≈ %v", ratio, math.Log(1001)/math.Log(2))
	}
}
