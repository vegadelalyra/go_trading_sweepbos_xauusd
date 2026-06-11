package xasset

import (
	"testing"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
)

// mkCandle is a tiny constructor that pins the time field to a
// monotonically increasing series so the tests can keep using OHL values
// as the only signal. Open/Close are derived from H/L so the candle is
// well-formed; the detector only reads High/Low.
func mkCandle(idx int, high, low float64) core.Candle {
	return core.Candle{
		Time:  time.Unix(int64(idx*300), 0).UTC(),
		Open:  (high + low) / 2,
		High:  high,
		Low:   low,
		Close: (high + low) / 2,
	}
}

// Synthesize a window that yields swing highs at indices 1 and 3 (HH)
// and swing lows at indices 2 and 4 (HL). Pattern:
//
//	idx: 0   1   2   3   4
//	hi:  10  12   9  14  10   ← swings at 1 (12) and 3 (14) → HH
//	lo:   8   6   3   5   2   ← wait — need rethinking.
//
// Easier: pad with 5 candles producing CLEAN HH+HL. Use OHL values:
//   - swing high #1 at idx 1: High=110, neighbours 100, 105
//   - swing low  #1 at idx 2: Low=95,  neighbours 105, 100
//   - swing high #2 at idx 3: High=115, neighbours 100, 102
//   - swing low  #2 at idx 4: Low=98,  neighbours 102, 100 (need a 5th)
//
// We need at least 6 bars to fit two swings on each side with neighbours.
func TestClassifyDXYStructure_Bullish(t *testing.T) {
	// HH (110 → 115) and HL (95 → 98).
	candles := []core.Candle{
		mkCandle(0, 105, 100), // pre-swing
		mkCandle(1, 110, 102), // swing-high #1 (110)
		mkCandle(2, 108, 95),  // swing-low  #1 (95)
		mkCandle(3, 115, 100), // swing-high #2 (115) → HH
		mkCandle(4, 112, 98),  // swing-low  #2 (98)  → HL
		mkCandle(5, 116, 105), // post-swing (anchors neighbour for idx 4)
	}
	if got := ClassifyDXYStructure(candles, 6); got != DXYBullish {
		t.Fatalf("want Bullish, got %v", got)
	}
}

func TestClassifyDXYStructure_Bearish(t *testing.T) {
	// LL (115 → 108) and LH (105 → 100). Each "swing" bar must have its
	// extreme strictly beating BOTH neighbours.
	candles := []core.Candle{
		mkCandle(0, 112, 108), // pre-swing  (H=112)
		mkCandle(1, 115, 110), // swing-high #1: H=115 beats 112,103 ✓
		mkCandle(2, 103, 105), // swing-low  #1: L=105 beats 110,112 ✓
		mkCandle(3, 108, 112), // swing-high #2: H=108 beats 103,105 ✓ → LL
		mkCandle(4, 105, 100), // swing-low  #2: L=100 beats 112,103 ✓ → LH
		mkCandle(5, 103, 102), // anchors neighbour for idx 4
	}
	if got := ClassifyDXYStructure(candles, 6); got != DXYBearish {
		t.Fatalf("want Bearish, got %v", got)
	}
}

// Mixed pattern: HH on the highs side but LH on the lows side → neutral.
func TestClassifyDXYStructure_Mixed(t *testing.T) {
	candles := []core.Candle{
		mkCandle(0, 105, 102),
		mkCandle(1, 110, 100), // swing-high #1 (110)
		mkCandle(2, 108, 95),  // swing-low  #1 (95)
		mkCandle(3, 115, 100), // swing-high #2 (115) → HH
		mkCandle(4, 112, 92),  // swing-low  #2 (92)  → LH (broken HL)
		mkCandle(5, 116, 100),
	}
	if got := ClassifyDXYStructure(candles, 6); got != DXYNeutral {
		t.Fatalf("want Neutral on mixed HH+LH, got %v", got)
	}
}

// Too few candles to identify two swings on each side.
func TestClassifyDXYStructure_NotEnoughBars(t *testing.T) {
	candles := []core.Candle{
		mkCandle(0, 100, 95),
		mkCandle(1, 105, 100),
		mkCandle(2, 102, 98),
	}
	if got := ClassifyDXYStructure(candles, 3); got != DXYNeutral {
		t.Fatalf("want Neutral on <5 bars, got %v", got)
	}
	if got := ClassifyDXYStructure(nil, 20); got != DXYNeutral {
		t.Fatalf("want Neutral on nil slice, got %v", got)
	}
}

// Non-positive lookback yields neutral by contract.
func TestClassifyDXYStructure_ZeroLookback(t *testing.T) {
	candles := []core.Candle{
		mkCandle(0, 100, 95), mkCandle(1, 110, 100), mkCandle(2, 105, 92),
		mkCandle(3, 115, 100), mkCandle(4, 112, 98), mkCandle(5, 116, 105),
	}
	if got := ClassifyDXYStructure(candles, 0); got != DXYNeutral {
		t.Fatalf("want Neutral on lookback=0, got %v", got)
	}
	if got := ClassifyDXYStructure(candles, -5); got != DXYNeutral {
		t.Fatalf("want Neutral on negative lookback, got %v", got)
	}
}

// Only one swing on each side (because the data plateaus / has no
// second turn) — must NOT classify, return neutral.
func TestClassifyDXYStructure_SingleSwing(t *testing.T) {
	// Single peak: only one swing high at idx 2, no second one.
	candles := []core.Candle{
		mkCandle(0, 100, 95),
		mkCandle(1, 102, 96),
		mkCandle(2, 110, 97), // lone swing-high
		mkCandle(3, 108, 96),
		mkCandle(4, 105, 95),
	}
	if got := ClassifyDXYStructure(candles, 5); got != DXYNeutral {
		t.Fatalf("want Neutral on single-swing data, got %v", got)
	}
}

// Lookback truncates to the most-recent N bars even when older data
// would have suggested a different posture. Construct a long series
// whose ANCIENT half is bullish and whose RECENT half is bearish; with
// n covering only the recent half we must get Bearish.
func TestClassifyDXYStructure_LookbackWindowsCorrectly(t *testing.T) {
	// Ancient half: bullish (idx 1..5 carry HH+HL).
	// Recent half: bearish (idx 7..11 carry LL+LH).
	candles := []core.Candle{
		mkCandle(0, 105, 100),
		mkCandle(1, 110, 102), // SH #1 (110 vs 105,108)
		mkCandle(2, 108, 95),  // SL #1 (95 vs 102,100)
		mkCandle(3, 115, 100), // SH #2 → HH (115 vs 108,112)
		mkCandle(4, 112, 98),  // SL #2 → HL (98 vs 100,105)
		mkCandle(5, 116, 105),
		// Recent half: bearish swings (mirror pattern of the bullish one).
		mkCandle(6, 112, 108),
		mkCandle(7, 135, 110), // SH #1 (135 vs 112,123)
		mkCandle(8, 123, 105), // SL #1 (105 vs 110,112)
		mkCandle(9, 128, 112), // SH #2 → LL (128 vs 123,125)
		mkCandle(10, 125, 100), // SL #2 → LH (100 vs 112,102)
		mkCandle(11, 123, 102),
	}
	// Full window: most-recent swings are in the bearish half — Bearish.
	if got := ClassifyDXYStructure(candles, len(candles)); got != DXYBearish {
		t.Fatalf("want Bearish on full window, got %v", got)
	}
	// Narrow window to ONLY the ancient half (n=6) — Bullish.
	if got := ClassifyDXYStructure(candles[:6], 6); got != DXYBullish {
		t.Fatalf("want Bullish on ancient-only window, got %v", got)
	}
}
