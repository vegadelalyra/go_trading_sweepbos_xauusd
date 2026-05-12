package meta

import (
	"testing"
	"time"
)

// Default config zero-value (Enabled=false) MUST be a strict no-op so
// threading the config through every ComposeSignal variant doesn't
// silently alter behavior for legacy callers. Same guarantee the London
// Fix config provides; the COMEX overlay would otherwise leak default
// behavior into every wfa / paper-live run that hasn't opted in.
func TestCOMEXPinConfig_DisabledIsNoop(t *testing.T) {
	cfg := COMEXPinConfig{} // zero-value, Enabled=false
	// Even with an expiry list populated, Enabled=false must short-circuit.
	cfg.ExpiryDates = []time.Time{time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)}
	cfg.PreWindowHours = 48
	cfg.MomentumMult = 0.0
	cfg.FadeMult = 1.3
	// A moment that WOULD be in-window if enabled.
	in := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	for _, strat := range []string{"DailyBreakout", "OrderBlockMit", "FVGFill"} {
		got := cfg.adjust(2.0, strat, in)
		if got != 2.0 {
			t.Fatalf("disabled config altered weight for %s: got %v, want 2.0", strat, got)
		}
	}
}

// DefaultCOMEXPinConfig MUST encode the canonical (48h, 0.0, 1.3) but
// stay disabled. Callers opt-in by flipping Enabled.
func TestDefaultCOMEXPinConfig_CanonicalParams(t *testing.T) {
	cfg := DefaultCOMEXPinConfig()
	if cfg.Enabled {
		t.Fatalf("Default config should be disabled (opt-in)")
	}
	if cfg.PreWindowHours != 48 {
		t.Fatalf("PreWindowHours = %d, want 48", cfg.PreWindowHours)
	}
	if cfg.MomentumMult != 0.0 {
		t.Fatalf("MomentumMult = %v, want 0.0", cfg.MomentumMult)
	}
	if cfg.FadeMult != 1.3 {
		t.Fatalf("FadeMult = %v, want 1.3", cfg.FadeMult)
	}
	if len(cfg.ExpiryDates) != 0 {
		t.Fatalf("ExpiryDates should be empty by default; LoadExpiryDates populates it")
	}
}

// In-window adjust: momentum strategies are silenced (×0), mean-reversion
// is boosted (×1.3), everything else passes through. The expiry is
// 2026-05-29 00:00 UTC; the 48h window opens at 2026-05-27 00:00 UTC.
func TestCOMEXPinConfig_AdjustInWindow(t *testing.T) {
	cfg := DefaultCOMEXPinConfig()
	cfg.Enabled = true
	cfg.ExpiryDates = []time.Time{time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)}
	in := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC) // 36h before expiry
	cases := []struct {
		strat string
		want  float64
	}{
		{"DailyBreakout", 0.0},      // momentum × 0.0
		{"AsianRangeBO", 0.0},       // momentum × 0.0
		{"BreakerBlock", 0.0},       // momentum × 0.0
		{"OrderBlockMit", 2.0 * 1.3}, // mean-reversion × 1.3
		{"OTEFibonacci", 2.0 * 1.3}, // mean-reversion × 1.3
		// Unaffected: neither momentum nor mean-reversion under this
		// classifier — JudasSwing is liquidity-fade but NOT the slow
		// pinning fade COMEX targets, so it passes through.
		{"JudasSwing", 2.0},
		{"FVGFill", 2.0},
		{"SilverBullet", 2.0},
	}
	for _, tc := range cases {
		got := cfg.adjust(2.0, tc.strat, in)
		if got != tc.want {
			t.Errorf("adjust(2.0, %q, in-window) = %v, want %v", tc.strat, got, tc.want)
		}
	}
}

// Out-of-window adjust: enabled config still passes weights through
// unchanged when t is outside every pre-expiry window. The boundary
// math is exercised in TestCOMEXPinConfig_HalfOpenBoundary; this case
// guards a moment FAR from any expiry.
func TestCOMEXPinConfig_AdjustOutOfWindow(t *testing.T) {
	cfg := DefaultCOMEXPinConfig()
	cfg.Enabled = true
	cfg.ExpiryDates = []time.Time{time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)}
	// 10 days before expiry — well outside the 48h pre-window.
	out := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	for _, strat := range []string{"DailyBreakout", "OrderBlockMit", "FVGFill"} {
		got := cfg.adjust(2.0, strat, out)
		if got != 2.0 {
			t.Errorf("out-of-window adjust(%s) = %v, want 2.0 (unchanged)", strat, got)
		}
	}
}

// Half-open boundary: window is [expiry - 48h, expiry). At exactly
// expiry-48h we ARE in; at exactly expiry we are OUT (post-pin).
// 1ns before expiry we are still IN; 1ns after expiry-48h we are IN.
func TestCOMEXPinConfig_HalfOpenBoundary(t *testing.T) {
	cfg := DefaultCOMEXPinConfig()
	cfg.Enabled = true
	expiry := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	cfg.ExpiryDates = []time.Time{expiry}
	pre := time.Duration(cfg.PreWindowHours) * time.Hour
	start := expiry.Add(-pre)

	cases := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"exact start (inclusive)", start, true},
		{"1ns after start", start.Add(time.Nanosecond), true},
		{"1ns before start (outside)", start.Add(-time.Nanosecond), false},
		{"middle of window", start.Add(24 * time.Hour), true},
		{"1ns before expiry (still in)", expiry.Add(-time.Nanosecond), true},
		{"exact expiry (exclusive)", expiry, false},
		{"1ns after expiry", expiry.Add(time.Nanosecond), false},
	}
	for _, tc := range cases {
		got := cfg.inWindow(tc.t)
		if got != tc.want {
			t.Errorf("%s: inWindow(%s) = %v, want %v", tc.name, tc.t, got, tc.want)
		}
	}
}

// Multiple expiries: a candle inside ANY pre-window triggers the
// adjustment. Tests the for-loop in inWindow against two months back-to-back.
func TestCOMEXPinConfig_MultipleExpiries(t *testing.T) {
	cfg := DefaultCOMEXPinConfig()
	cfg.Enabled = true
	cfg.ExpiryDates = []time.Time{
		time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC),
	}
	// Inside the April pre-window only.
	tApr := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	if !cfg.inWindow(tApr) {
		t.Errorf("expected %s to be inside April pre-window", tApr)
	}
	// Inside the May pre-window only.
	tMay := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	if !cfg.inWindow(tMay) {
		t.Errorf("expected %s to be inside May pre-window", tMay)
	}
	// Between the two windows — should be out.
	tMid := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	if cfg.inWindow(tMid) {
		t.Errorf("expected %s to be OUTSIDE both pre-windows", tMid)
	}
}

// LastBusinessDayOfMonth: end-of-month that lands on Saturday rolls back
// to Friday; Sunday rolls back to Friday too. A normal weekday last day
// stays put.
func TestLastBusinessDayOfMonth(t *testing.T) {
	cases := []struct {
		year  int
		month time.Month
		want  string // yyyy-mm-dd
	}{
		// 2024-05-31 is a Friday — stays put.
		{2024, time.May, "2024-05-31"},
		// 2024-08-31 is a Saturday — rolls back to Friday 2024-08-30.
		{2024, time.August, "2024-08-30"},
		// 2025-08-31 is a Sunday — rolls back to Friday 2025-08-29.
		{2025, time.August, "2025-08-29"},
		// 2026-05-31 is a Sunday — rolls back to Friday 2026-05-29.
		{2026, time.May, "2026-05-29"},
	}
	for _, tc := range cases {
		got := LastBusinessDayOfMonth(tc.year, tc.month)
		want, _ := time.Parse("2006-01-02", tc.want)
		if !got.Equal(want) {
			t.Errorf("LastBusinessDayOfMonth(%d,%v) = %s, want %s",
				tc.year, tc.month, got.Format("2006-01-02"), tc.want)
		}
	}
}
