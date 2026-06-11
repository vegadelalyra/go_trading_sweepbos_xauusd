package meta

import (
	"sort"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/xasset"
)

// DXYFilterConfig parametrises the intermarket-divergence weight
// adjustment applied per XAU signal at vote time.
//
// Formula (Phase 3.3):
//   - XAU LONG  + DXY BEARISH  →  weight × ConfluentMult  (default 1.3)
//   - XAU SHORT + DXY BULLISH  →  weight × ConfluentMult
//   - XAU LONG  + DXY BULLISH  →  weight × OpposingMult   (default 0.3)
//   - XAU SHORT + DXY BEARISH  →  weight × OpposingMult
//   - DXY NEUTRAL or no data   →  weight × 1.0 (no change)
//
// Microstructural rationale: XAU/USD has a strong inverse correlation
// with the US Dollar Index (DXY). A LONG into a structurally bullish
// DXY is fighting macro flow; the same LONG into a bearish DXY rides
// it. Multiplying weight asymmetrically (boost confluent / penalise
// opposing) lets the composite vote demand a stronger consensus to
// open trades against the macro tape.
//
// The DXYCandles slice is populated ONCE at startup from the ohlc
// table and is keyed in `cache` (built lazily on first lookup) by the
// bar timestamp truncated to the interval. A binary search over the
// candle slice keeps adjustForXAU O(log N) per call — for a 40k-candle
// 2y history that's ~16 comparisons, negligible against the rest of
// composeCore.
//
// Default zero-value (Enabled=false) is a no-op so threading the
// config through every ComposeSignal variant is non-invasive.
type DXYFilterConfig struct {
	Enabled       bool
	LookbackBars  int     // window passed to ClassifyDXYStructure (default 20)
	ConfluentMult float64 // weight multiplier when XAU side aligns inverse-to-DXY
	OpposingMult  float64 // weight multiplier when XAU side fights DXY structure
	// DXYCandles holds the DXY series in chronological order. Empty when
	// Enabled=false. Populated by the CLI loader before constructing the
	// config; downstream code never appends to it.
	DXYCandles []core.Candle
	// structureCache memoises the DXYStructure for the bar ENDING at
	// each index of DXYCandles. Lazily filled in adjustForXAU's first
	// call — the cost of computing 20-bar swings on every XAU candle
	// would otherwise dominate the inner loop.
	structureCache []DXYStructureSlot
}

// DXYStructureSlot is a memo entry for the structure-at-bar-N cache.
// Filled bit fields keep the slice compact (no map overhead): a
// 40k-candle cache fits in ~640 KiB.
type DXYStructureSlot struct {
	Computed  bool
	Structure xasset.DXYStructure
}

// DefaultDXYFilterConfig returns the canonical Phase 3.3 parameters
// with Enabled=false. Callers opt-in by flipping Enabled to true AND
// providing a populated DXYCandles slice.
func DefaultDXYFilterConfig() DXYFilterConfig {
	return DXYFilterConfig{
		Enabled:       false,
		LookbackBars:  20,
		ConfluentMult: 1.3,
		OpposingMult:  0.3,
	}
}

// adjustForXAU applies the multiplier to weight `w` based on the XAU
// candidate side and the DXY structure observed up-to-and-including
// time `t`. Returns `w` unchanged when the filter is disabled, when
// the DXY series is empty, when t precedes the earliest DXY bar, or
// when the structure is neutral. This is the single hot-path entry
// point invoked by composeCore — keep it allocation-free.
func (d *DXYFilterConfig) adjustForXAU(w float64, xauSide core.Side, t time.Time) float64 {
	if !d.Enabled || w <= 0 || len(d.DXYCandles) == 0 {
		return w
	}
	idx := dxyBarIndex(d.DXYCandles, t)
	if idx < 0 {
		// t precedes the earliest DXY bar; no posture available.
		return w
	}
	// Lazy-init the cache. Safe under the WFA/paper-live model: this
	// config is constructed by the CLI and passed by value into the
	// engine, where the cache is touched serially per OOS window.
	// We DO mutate via pointer receiver to retain the cache between
	// composeCore calls; callers must pass a pointer (composeCore now
	// takes DXYFilterConfig by value but composeCore mutates via &dxy
	// just like LondonFix is passed by value but read-only).
	//
	// In practice the structure is recomputed once per DXY bar across
	// the whole run, even though the same DXY bar will be looked up
	// hundreds of times by consecutive XAU candles inside that bar's
	// time slice — exactly the property the cache buys.
	if d.structureCache == nil {
		d.structureCache = make([]DXYStructureSlot, len(d.DXYCandles))
	}
	slot := &d.structureCache[idx]
	if !slot.Computed {
		// Look at the last LookbackBars candles ending at idx (inclusive).
		start := idx + 1 - d.LookbackBars
		if start < 0 {
			start = 0
		}
		slot.Structure = xasset.ClassifyDXYStructure(d.DXYCandles[start:idx+1], d.LookbackBars)
		slot.Computed = true
	}
	switch slot.Structure {
	case xasset.DXYBullish:
		if xauSide == core.Short {
			return w * d.ConfluentMult
		}
		return w * d.OpposingMult
	case xasset.DXYBearish:
		if xauSide == core.Long {
			return w * d.ConfluentMult
		}
		return w * d.OpposingMult
	default: // DXYNeutral
		return w
	}
}

// dxyBarIndex returns the index of the most recent DXY candle whose
// Time is <= t. Returns -1 when t precedes the entire series. Uses
// binary search — the DXY slice is sorted ascending by construction
// (loadCandles ORDER BY bar_time ASC).
func dxyBarIndex(candles []core.Candle, t time.Time) int {
	if len(candles) == 0 {
		return -1
	}
	// sort.Search returns the smallest i such that candles[i].Time > t.
	// The bar we want is the predecessor, i-1.
	i := sort.Search(len(candles), func(k int) bool {
		return candles[k].Time.After(t)
	})
	return i - 1
}
