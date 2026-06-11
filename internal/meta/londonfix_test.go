package meta

import (
	"testing"
	"time"
)

// Default config zero-value (Enabled=false) MUST be a strict no-op so
// threading the config through every ComposeSignal variant doesn't
// silently alter behavior for legacy callers.
func TestLondonFixConfig_DisabledIsNoop(t *testing.T) {
	cfg := LondonFixConfig{} // zero-value, Enabled=false
	// Pick a time deep inside the canonical window — the disabled config
	// should NOT touch the weight.
	in := time.Date(2026, 5, 11, 15, 0, 0, 0, time.UTC)
	for _, strat := range []string{"DailyBreakout", "JudasSwing", "FVGFill"} {
		got := cfg.adjust(2.0, strat, in)
		if got != 2.0 {
			t.Fatalf("disabled config altered weight for %s: got %v, want 2.0", strat, got)
		}
	}
}

// Default config returned by DefaultLondonFixConfig should encode the
// canonical (14:40, 15:20, 0.3, 1.5) but still be disabled — opt-in.
func TestDefaultLondonFixConfig_CanonicalParams(t *testing.T) {
	cfg := DefaultLondonFixConfig()
	if cfg.Enabled {
		t.Fatalf("Default config should be disabled (opt-in)")
	}
	if cfg.StartHour != 14 || cfg.StartMin != 40 {
		t.Fatalf("Start = %02d:%02d, want 14:40", cfg.StartHour, cfg.StartMin)
	}
	if cfg.EndHour != 15 || cfg.EndMin != 20 {
		t.Fatalf("End = %02d:%02d, want 15:20", cfg.EndHour, cfg.EndMin)
	}
	if cfg.MomentumMult != 0.3 {
		t.Fatalf("MomentumMult = %v, want 0.3", cfg.MomentumMult)
	}
	if cfg.FadeMult != 1.5 {
		t.Fatalf("FadeMult = %v, want 1.5", cfg.FadeMult)
	}
}

// Window membership: 14:39 is OUT, 14:40 is IN (inclusive start),
// 15:19 is IN, 15:20 is OUT (exclusive end). Half-open interval.
func TestLondonFixConfig_WindowMembership(t *testing.T) {
	cfg := DefaultLondonFixConfig()
	cfg.Enabled = true
	cases := []struct {
		hour, min int
		want      bool
	}{
		{14, 39, false},
		{14, 40, true},
		{14, 59, true},
		{15, 0, true},
		{15, 19, true},
		{15, 20, false},
		{0, 0, false},
		{23, 59, false},
	}
	for _, tc := range cases {
		ts := time.Date(2026, 5, 11, tc.hour, tc.min, 0, 0, time.UTC)
		got := cfg.inWindow(ts)
		if got != tc.want {
			t.Errorf("inWindow(%02d:%02d) = %v, want %v", tc.hour, tc.min, got, tc.want)
		}
	}
}

// adjust applies MomentumMult to known momentum strategies and FadeMult
// to known fade strategies inside the window — and leaves other strategies
// (e.g. FVGFill, OrderBlockMit) untouched.
func TestLondonFixConfig_AdjustInWindow(t *testing.T) {
	cfg := DefaultLondonFixConfig()
	cfg.Enabled = true
	in := time.Date(2026, 5, 11, 15, 0, 0, 0, time.UTC) // mid-window
	cases := []struct {
		strat string
		want  float64
	}{
		{"DailyBreakout", 2.0 * 0.3},
		{"AsianRangeBO", 2.0 * 0.3},
		{"BreakerBlock", 2.0 * 0.3},
		{"JudasSwing", 2.0 * 1.5},
		{"TurtleSoup", 2.0 * 1.5},
		{"LiquiditySweepBoS", 2.0 * 1.5},
		{"SilverBullet", 2.0 * 1.5},
		// Unaffected: neither momentum nor fade.
		{"FVGFill", 2.0},
		{"OrderBlockMit", 2.0},
		{"EMACross", 2.0},
	}
	for _, tc := range cases {
		got := cfg.adjust(2.0, tc.strat, in)
		if got != tc.want {
			t.Errorf("adjust(2.0, %q, in-window) = %v, want %v", tc.strat, got, tc.want)
		}
	}
}

// Outside the window, even enabled config should pass weights through
// unchanged regardless of strategy.
func TestLondonFixConfig_AdjustOutOfWindow(t *testing.T) {
	cfg := DefaultLondonFixConfig()
	cfg.Enabled = true
	out := time.Date(2026, 5, 11, 8, 0, 0, 0, time.UTC) // far from window
	for _, strat := range []string{"DailyBreakout", "JudasSwing", "FVGFill"} {
		got := cfg.adjust(2.0, strat, out)
		if got != 2.0 {
			t.Errorf("out-of-window adjust(%s) = %v, want 2.0 (unchanged)", strat, got)
		}
	}
}
