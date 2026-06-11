package meta

import (
	"math"
	"testing"
)

func weightOfStrat(ws []StrategyWeight, name string) (StrategyWeight, bool) {
	for _, w := range ws {
		if w.Strategy == name {
			return w, true
		}
	}
	return StrategyWeight{}, false
}

// TestBlendLiveWeights covers the shrinkage math and every guard.
func TestBlendLiveWeights(t *testing.T) {
	canon := []StrategyWeight{
		{Strategy: "SilverBullet", Trades: 800, Weight: 2.0},
		{Strategy: "OrderBlockMit", Trades: 400, Weight: 1.0},
		{Strategy: "FVGFill", Trades: 300, Weight: 0.0}, // decayed/negative → 0
		{Strategy: "DailyBreakout", Trades: 36, Weight: 0.5},
	}
	live := []StrategyWeight{
		{Strategy: "SilverBullet", Trades: 200, Weight: 4.0},  // n == K → λ=0.5
		{Strategy: "OrderBlockMit", Trades: 10, Weight: 0.1},  // below floor → ignored
		{Strategy: "FVGFill", Trades: 600, Weight: 3.0},       // canon 0 → no resurrect
		{Strategy: "DailyBreakout", Trades: 200, Weight: 0.0}, // adverse live → lowers, not silences
	}
	k, floor := 200.0, 20

	out := BlendLiveWeights(canon, live, k, floor)

	// SilverBullet: λ = 200/(200+200) = 0.5 → 0.5*2.0 + 0.5*4.0 = 3.0
	if w, _ := weightOfStrat(out, "SilverBullet"); math.Abs(w.Weight-3.0) > 1e-9 {
		t.Fatalf("SilverBullet blend = %.6f, want 3.0", w.Weight)
	}
	// OrderBlockMit: only 10 live trades < floor 20 → untouched canonical 1.0
	if w, _ := weightOfStrat(out, "OrderBlockMit"); math.Abs(w.Weight-1.0) > 1e-9 {
		t.Fatalf("OrderBlockMit (below floor) = %.6f, want 1.0 (untouched)", w.Weight)
	}
	// FVGFill: canonical weight 0 → never resurrected even with strong live edge
	if w, _ := weightOfStrat(out, "FVGFill"); w.Weight != 0 {
		t.Fatalf("FVGFill (canon 0) = %.6f, want 0 (no resurrect)", w.Weight)
	}
	// DailyBreakout: adverse live (w=0), λ=0.5 → 0.5*0.5 + 0.5*0 = 0.25 (halved, alive)
	if w, _ := weightOfStrat(out, "DailyBreakout"); math.Abs(w.Weight-0.25) > 1e-9 {
		t.Fatalf("DailyBreakout (adverse live) = %.6f, want 0.25 (lowered, not silenced)", w.Weight)
	}

	// Input slices must not be mutated.
	if canon[0].Weight != 2.0 {
		t.Fatalf("canon slice was mutated: SilverBullet now %.6f", canon[0].Weight)
	}
}

// TestBlendLiveWeights_NoLiveIsIdentity: with no live trades the blend is a
// no-op — exactly the canonical, so feedback is inert-safe until N accumulates.
func TestBlendLiveWeights_NoLiveIsIdentity(t *testing.T) {
	canon := []StrategyWeight{
		{Strategy: "SilverBullet", Trades: 800, Weight: 2.13},
		{Strategy: "OrderBlockMit", Trades: 400, Weight: 2.24},
	}
	out := BlendLiveWeights(canon, nil, 200, 20)
	for _, c := range canon {
		w, ok := weightOfStrat(out, c.Strategy)
		if !ok || w.Weight != c.Weight {
			t.Fatalf("%s: blend changed weight with no live data (%.6f → %.6f)", c.Strategy, c.Weight, w.Weight)
		}
	}
}

// TestBlendLiveWeights_LambdaMonotonic: more live trades → live opinion earns
// more say. With live weight above canonical, the blended weight rises with n.
func TestBlendLiveWeights_LambdaMonotonic(t *testing.T) {
	canon := []StrategyWeight{{Strategy: "S", Trades: 500, Weight: 1.0}}
	prev := 1.0
	for _, n := range []int{20, 50, 100, 200, 500, 2000} {
		out := BlendLiveWeights(canon,
			[]StrategyWeight{{Strategy: "S", Trades: n, Weight: 5.0}}, 200, 20)
		w, _ := weightOfStrat(out, "S")
		if w.Weight < prev {
			t.Fatalf("n=%d: blended weight %.4f dropped below previous %.4f (should rise toward live)", n, w.Weight, prev)
		}
		if w.Weight > 5.0 {
			t.Fatalf("n=%d: blended weight %.4f exceeded live weight 5.0", n, w.Weight)
		}
		prev = w.Weight
	}
}
