package meta

import (
	"testing"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/xasset"
)

// mkDXY constructs synthetic DXY candles spaced 5m apart. The High/Low
// pattern is chosen to produce a deterministic structural posture on
// the trailing 20-bar window.
func mkDXY(values [][2]float64, t0 time.Time) []core.Candle {
	out := make([]core.Candle, len(values))
	for i, v := range values {
		out[i] = core.Candle{
			Time:  t0.Add(time.Duration(i) * 5 * time.Minute),
			Open:  (v[0] + v[1]) / 2,
			High:  v[0],
			Low:   v[1],
			Close: (v[0] + v[1]) / 2,
		}
	}
	return out
}

// Default zero-value (Enabled=false) is a strict no-op. Adjusting any
// weight returns the input unchanged regardless of XAU side or time.
func TestDXYFilterConfig_DisabledIsNoop(t *testing.T) {
	cfg := DXYFilterConfig{} // zero value
	when := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	if got := cfg.adjustForXAU(2.0, core.Long, when); got != 2.0 {
		t.Fatalf("disabled config altered weight: got %v", got)
	}
	if got := cfg.adjustForXAU(2.0, core.Short, when); got != 2.0 {
		t.Fatalf("disabled config altered weight (short): got %v", got)
	}
}

func TestDefaultDXYFilterConfig_CanonicalParams(t *testing.T) {
	cfg := DefaultDXYFilterConfig()
	if cfg.Enabled {
		t.Fatalf("default should be opt-in (Enabled=false)")
	}
	if cfg.LookbackBars != 20 {
		t.Fatalf("LookbackBars = %d, want 20", cfg.LookbackBars)
	}
	if cfg.ConfluentMult != 1.3 {
		t.Fatalf("ConfluentMult = %v, want 1.3", cfg.ConfluentMult)
	}
	if cfg.OpposingMult != 0.3 {
		t.Fatalf("OpposingMult = %v, want 0.3", cfg.OpposingMult)
	}
}

// Bullish DXY + XAU LONG → opposing (×0.3); + XAU SHORT → confluent (×1.3).
func TestDXYFilter_BullishDXY_AppliesAsymmetry(t *testing.T) {
	t0 := time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC)
	// Bullish swing pattern: HH (110→115) + HL (95→98).
	candles := mkDXY([][2]float64{
		{105, 100},
		{110, 102}, // SH#1
		{108, 95},  // SL#1
		{115, 100}, // SH#2 → HH
		{112, 98},  // SL#2 → HL
		{116, 105},
	}, t0)
	cfg := DefaultDXYFilterConfig()
	cfg.Enabled = true
	cfg.LookbackBars = 6
	cfg.DXYCandles = candles
	// At the time of the LAST candle, structure must be Bullish.
	tLast := candles[len(candles)-1].Time
	if got := cfg.adjustForXAU(2.0, core.Long, tLast); got != 2.0*0.3 {
		t.Errorf("Long vs Bullish DXY: got %v, want %v", got, 2.0*0.3)
	}
	if got := cfg.adjustForXAU(2.0, core.Short, tLast); got != 2.0*1.3 {
		t.Errorf("Short vs Bullish DXY: got %v, want %v", got, 2.0*1.3)
	}
	// Sanity: structure was actually bullish (sneak peek into xasset).
	if s := xasset.ClassifyDXYStructure(candles, 6); s != xasset.DXYBullish {
		t.Fatalf("test data did not produce Bullish: %v", s)
	}
}

// Bearish DXY + XAU LONG → confluent (×1.3); + XAU SHORT → opposing (×0.3).
func TestDXYFilter_BearishDXY_AppliesAsymmetry(t *testing.T) {
	t0 := time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC)
	// Bearish swing pattern: LL (115→108) + LH (105→100).
	candles := mkDXY([][2]float64{
		{112, 108},
		{115, 110}, // SH#1
		{103, 105}, // SL#1
		{108, 112}, // SH#2 → LL
		{105, 100}, // SL#2 → LH
		{103, 102},
	}, t0)
	cfg := DefaultDXYFilterConfig()
	cfg.Enabled = true
	cfg.LookbackBars = 6
	cfg.DXYCandles = candles
	tLast := candles[len(candles)-1].Time
	if got := cfg.adjustForXAU(2.0, core.Long, tLast); got != 2.0*1.3 {
		t.Errorf("Long vs Bearish DXY: got %v, want %v", got, 2.0*1.3)
	}
	if got := cfg.adjustForXAU(2.0, core.Short, tLast); got != 2.0*0.3 {
		t.Errorf("Short vs Bearish DXY: got %v, want %v", got, 2.0*0.3)
	}
}

// Neutral DXY structure → multiplier 1.0 regardless of side.
func TestDXYFilter_NeutralDXY_NoAdjustment(t *testing.T) {
	t0 := time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC)
	// Mixed pattern (from xasset Mixed test): HH + LH = neutral.
	candles := mkDXY([][2]float64{
		{105, 102},
		{110, 100}, // SH#1
		{108, 95},  // SL#1
		{115, 100}, // SH#2 → HH
		{112, 92},  // SL#2 → LH (broken HL)
		{116, 100},
	}, t0)
	cfg := DefaultDXYFilterConfig()
	cfg.Enabled = true
	cfg.LookbackBars = 6
	cfg.DXYCandles = candles
	tLast := candles[len(candles)-1].Time
	for _, side := range []core.Side{core.Long, core.Short} {
		if got := cfg.adjustForXAU(2.0, side, tLast); got != 2.0 {
			t.Errorf("Neutral DXY side=%v: got %v, want 2.0", side, got)
		}
	}
}

// XAU candle time precedes the earliest DXY bar → no adjustment.
func TestDXYFilter_BeforeFirstDXYBar(t *testing.T) {
	t0 := time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC)
	candles := mkDXY([][2]float64{
		{105, 100}, {110, 102}, {108, 95},
		{115, 100}, {112, 98}, {116, 105},
	}, t0)
	cfg := DefaultDXYFilterConfig()
	cfg.Enabled = true
	cfg.LookbackBars = 6
	cfg.DXYCandles = candles
	// 1h before first DXY bar.
	before := t0.Add(-time.Hour)
	if got := cfg.adjustForXAU(2.0, core.Long, before); got != 2.0 {
		t.Errorf("before-first-bar Long: got %v, want 2.0 (no data)", got)
	}
	if got := cfg.adjustForXAU(2.0, core.Short, before); got != 2.0 {
		t.Errorf("before-first-bar Short: got %v, want 2.0 (no data)", got)
	}
}

// XAU candle time falls between DXY bars → uses the predecessor.
func TestDXYFilter_GappedLookupUsesPredecessor(t *testing.T) {
	t0 := time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC)
	candles := mkDXY([][2]float64{
		{105, 100}, {110, 102}, {108, 95},
		{115, 100}, {112, 98}, {116, 105},
	}, t0)
	cfg := DefaultDXYFilterConfig()
	cfg.Enabled = true
	cfg.LookbackBars = 6
	cfg.DXYCandles = candles
	// Halfway between bar 4 and bar 5 — the predecessor is bar 5
	// (last one, structure = bullish).
	// candles[5].Time is t0 + 5*5m = 25m later. Pick 27m later.
	tQuery := t0.Add(27 * time.Minute)
	if got := cfg.adjustForXAU(2.0, core.Long, tQuery); got != 2.0*0.3 {
		t.Errorf("gapped Long vs Bullish DXY: got %v, want %v", got, 2.0*0.3)
	}
}

// Cache must be lazily built and reused — calling twice produces the
// same answer and does not regrow the cache slice.
func TestDXYFilter_CacheConsistency(t *testing.T) {
	t0 := time.Date(2026, 5, 11, 9, 0, 0, 0, time.UTC)
	candles := mkDXY([][2]float64{
		{105, 100}, {110, 102}, {108, 95},
		{115, 100}, {112, 98}, {116, 105},
	}, t0)
	cfg := DefaultDXYFilterConfig()
	cfg.Enabled = true
	cfg.LookbackBars = 6
	cfg.DXYCandles = candles
	tLast := candles[len(candles)-1].Time
	first := cfg.adjustForXAU(2.0, core.Long, tLast)
	second := cfg.adjustForXAU(2.0, core.Long, tLast)
	if first != second {
		t.Errorf("cache inconsistent: first=%v second=%v", first, second)
	}
	if len(cfg.structureCache) != len(candles) {
		t.Errorf("cache len = %d, want %d", len(cfg.structureCache), len(candles))
	}
}

// Empty DXYCandles slice with Enabled=true should still no-op (the
// production loader could legitimately produce an empty result if the
// query window misses the DXY backfill).
func TestDXYFilter_EnabledButEmptyCandles(t *testing.T) {
	cfg := DefaultDXYFilterConfig()
	cfg.Enabled = true
	cfg.LookbackBars = 20
	cfg.DXYCandles = nil
	when := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	if got := cfg.adjustForXAU(2.0, core.Long, when); got != 2.0 {
		t.Fatalf("enabled-but-empty: got %v, want 2.0", got)
	}
}
