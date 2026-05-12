package engine

import (
	"testing"
	"time"
)

// nyTime builds a UTC time that lands on the given NY clock hour. Uses a
// canonical 2025-06-15 date so the NY DST is in force (UTC-04) — keeps
// the hour arithmetic mechanical for the test.
func nyTime(hourNY int) time.Time {
	// 2025-06-15 is a Sunday; pick a Monday so the session is open.
	return time.Date(2025, 6, 16, hourNY+4, 0, 0, 0, time.UTC)
}

// Disabled curve must echo the fallback unchanged — the legacy path that
// every existing tuning relies on.
func TestSpreadCurve_DisabledReturnsFallback(t *testing.T) {
	sc := SpreadCurveConfig{Enabled: false, Base: 0.32}
	for _, hour := range []int{0, 8, 17, 23} {
		got := sc.EffectiveSpread(nyTime(hour), 0.50)
		if got != 0.50 {
			t.Fatalf("disabled curve returned %v at NY hour %d, want fallback 0.50", got, hour)
		}
	}
}

// Quiet NY hour (e.g., 04:00) uses the base spread.
func TestSpreadCurve_BaseDuringQuietHour(t *testing.T) {
	sc := DefaultSpreadCurveConfig()
	sc.Enabled = true
	got := sc.EffectiveSpread(nyTime(4), 0.50)
	if got != sc.Base {
		t.Fatalf("quiet-hour spread = %v, want %v", got, sc.Base)
	}
}

// 17:00 NY rollover applies the RolloverMult.
func TestSpreadCurve_RolloverApplies(t *testing.T) {
	sc := DefaultSpreadCurveConfig()
	sc.Enabled = true
	got := sc.EffectiveSpread(nyTime(17), 0.50)
	want := sc.Base * sc.RolloverMult
	if got != want {
		t.Fatalf("17:00 NY spread = %v, want %v", got, want)
	}
}

// NY 08/09/10/14 hours apply the NewsMult.
func TestSpreadCurve_NewsMultApplies(t *testing.T) {
	sc := DefaultSpreadCurveConfig()
	sc.Enabled = true
	want := sc.Base * sc.NewsMult
	for _, hour := range []int{8, 9, 10, 14} {
		got := sc.EffectiveSpread(nyTime(hour), 0.50)
		if got != want {
			t.Fatalf("NY %d:00 spread = %v, want %v", hour, got, want)
		}
	}
}

// SpreadSpike must return false when the filter is disabled.
func TestSpreadCurve_SpikeDisabledIsFalse(t *testing.T) {
	sc := DefaultSpreadCurveConfig()
	sc.Enabled = true
	sc.FilterEnabled = false
	if sc.SpreadSpike(nyTime(17), 5*time.Minute) {
		t.Fatal("filter disabled, SpreadSpike returned true")
	}
}

// SpreadSpike fires at 17:00 NY: the rollover multiplier × Base
// (1.0 = 3 × 0.32 = 0.96) is well above the 6-bar trailing SMA at 16:30-16:55
// which is just 6 × Base = ~0.32, so 0.96 > 1.5 × 0.32 = 0.48 → spike.
func TestSpreadCurve_SpikeFiresAtRollover(t *testing.T) {
	sc := DefaultSpreadCurveConfig()
	sc.Enabled = true
	sc.FilterEnabled = true
	if !sc.SpreadSpike(nyTime(17), 5*time.Minute) {
		t.Fatal("17:00 NY should trip the spread-spike filter")
	}
	// Mid-session quiet hour (e.g., NY 04:00) must NOT trip the filter —
	// every trailing bar is at Base, so ratio is exactly 1.0.
	if sc.SpreadSpike(nyTime(4), 5*time.Minute) {
		t.Fatal("quiet hour should not trip the spike filter")
	}
}
