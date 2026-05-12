package smc

import (
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
)

// IncrementalAnalyzer rebuilds the same Analysis as Analyze, but amortises
// the per-bar cost: each new candle adds at most a swing, an FVG, two
// structure events and two order blocks — all bounded back-lookups. The
// batch version is O(N²) on order-block sweep checks and is unusable in
// paper-live with 165k+ candles where each poll only adds one bar.
//
// All primitives are append-only by construction with one subtlety:
// DetectOrderBlocks' "FVG within obIdx+3" pillar peeks up to 2 candles
// past the structure event, so an event at e.Index isn't safe to finalise
// into an OrderBlock until candle e.Index+2 has been appended. Events in
// that look-ahead window live in `pendingEvents` and are evaluated lazily
// inside Analysis() with whatever FVGs exist at call time — which exactly
// matches what Analyze(candles[:len()]) would produce. See
// TestIncrementalParity_Append.
//
// Not safe for concurrent use. paper.RunLive owns one analyzer for the
// duration of the loop; wfa / paper-replay keep using batch Analyze.
type IncrementalAnalyzer struct {
	candles       []core.Candle
	swings        []Swing
	fvgs          []FVG
	structure     []StructureEvent
	orderBlocks   []OrderBlock     // OBs whose look-ahead window has closed
	pendingEvents []StructureEvent // events still inside the +2-bar OB window
	lastBOSDir    int              // mirrors the variable inside DetectStructure
}

// NewIncrementalAnalyzer returns an empty analyzer. Use Reset to seed it
// from an existing candle history, or feed candles one at a time via Append.
func NewIncrementalAnalyzer() *IncrementalAnalyzer { return &IncrementalAnalyzer{} }

// Reset replays the entire candle slice through Append, leaving the
// analyzer in the same state as Analyze(candles). Use this on startup and
// whenever a gap / desync is detected.
func (a *IncrementalAnalyzer) Reset(candles []core.Candle) {
	a.candles = a.candles[:0]
	a.swings = a.swings[:0]
	a.fvgs = a.fvgs[:0]
	a.structure = a.structure[:0]
	a.orderBlocks = a.orderBlocks[:0]
	a.pendingEvents = a.pendingEvents[:0]
	a.lastBOSDir = 0
	for _, c := range candles {
		a.Append(c)
	}
}

// Len returns the number of candles currently held.
func (a *IncrementalAnalyzer) Len() int { return len(a.candles) }

// LastTime returns the time of the most recent appended candle, or the
// zero time if the analyzer is empty. Callers use this to detect gaps
// before calling Append (a gap means at least one bar was missed and the
// analyzer must be rebuilt via Reset, not patched bar-by-bar).
func (a *IncrementalAnalyzer) LastTime() time.Time {
	if n := len(a.candles); n > 0 {
		return a.candles[n-1].Time
	}
	return time.Time{}
}

// Analysis returns a snapshot identical (bit-for-bit) to smc.Analyze run
// on the same candle sequence. Finalised primitives alias internal state
// directly; the OrderBlocks slice is freshly allocated when there are
// still pending events so the caller can read it without racing future
// Append calls.
func (a *IncrementalAnalyzer) Analysis() Analysis {
	obs := a.orderBlocks
	if len(a.pendingEvents) > 0 {
		// Tentatively evaluate the trailing events with the FVG / swing
		// state currently known. Matches what Analyze would emit if it
		// were called on candles[:a.Len()] right now.
		out := make([]OrderBlock, len(a.orderBlocks), len(a.orderBlocks)+len(a.pendingEvents))
		copy(out, a.orderBlocks)
		for _, e := range a.pendingEvents {
			if ob, ok := a.detectOBForEvent(e); ok {
				out = append(out, ob)
			}
		}
		obs = out
	}
	return Analysis{
		Swings:      a.swings,
		FVGs:        a.fvgs,
		Structure:   a.structure,
		OrderBlocks: obs,
	}
}

// Append adds one candle and updates every derived primitive. The order
// of work mirrors DetectSwings → DetectFVGs → DetectStructure → DetectOrderBlocks
// so that any new structure event sees the same swings/fvgs the batch
// version would at the same point.
func (a *IncrementalAnalyzer) Append(c core.Candle) {
	a.candles = append(a.candles, c)
	i := len(a.candles) - 1

	// ── 1. Swing confirmation at index i-1 ─────────────────────────────
	// DetectSwings only fires for 1 <= k <= len-2, i.e. once candle k+1
	// is present. The newly appended candle at index i lets us confirm
	// a swing at k = i-1, provided i-1 >= 1.
	if i >= 2 {
		k := i - 1
		if a.candles[k].High > a.candles[k-1].High && a.candles[k].High > a.candles[i].High {
			a.swings = append(a.swings, Swing{Index: k, Price: a.candles[k].High, IsHigh: true})
		}
		if a.candles[k].Low < a.candles[k-1].Low && a.candles[k].Low < a.candles[i].Low {
			a.swings = append(a.swings, Swing{Index: k, Price: a.candles[k].Low, IsHigh: false})
		}
	}

	// ── 2. FVG confirmation at index i ─────────────────────────────────
	// DetectFVGs fires for 2 <= i, comparing candle i-2 and candle i.
	if i >= 2 {
		x := a.candles[i-2]
		y := a.candles[i]
		if x.High < y.Low {
			a.fvgs = append(a.fvgs, FVG{Index: i, Bottom: x.High, Top: y.Low, Bullish: true})
		} else if x.Low > y.High {
			a.fvgs = append(a.fvgs, FVG{Index: i, Bottom: y.High, Top: x.Low, Bullish: false})
		}
	}

	// ── 3. Structure detection at index i ──────────────────────────────
	// DetectStructure has a "return early if len(swings)==0" guard; we
	// replicate it implicitly because the nearest-swing search below
	// will find nothing and emit no events when swings is empty.
	if len(a.swings) > 0 {
		var nearestHigh, nearestLow *Swing
		for s := range a.swings {
			sw := &a.swings[s]
			if sw.Index >= i {
				break
			}
			if sw.IsHigh {
				nearestHigh = sw
			} else {
				nearestLow = sw
			}
		}
		if nearestHigh != nil && a.candles[i].Close > nearestHigh.Price {
			kind := StructBOSBull
			if a.lastBOSDir == -1 {
				kind = StructCHoChBull
			}
			ev := StructureEvent{Index: i, Kind: kind, SwingIndex: nearestHigh.Index, SwingPrice: nearestHigh.Price}
			a.structure = append(a.structure, ev)
			a.pendingEvents = append(a.pendingEvents, ev)
			a.lastBOSDir = 1
		}
		if nearestLow != nil && a.candles[i].Close < nearestLow.Price {
			kind := StructBOSBear
			if a.lastBOSDir == 1 {
				kind = StructCHoChBear
			}
			ev := StructureEvent{Index: i, Kind: kind, SwingIndex: nearestLow.Index, SwingPrice: nearestLow.Price}
			a.structure = append(a.structure, ev)
			a.pendingEvents = append(a.pendingEvents, ev)
			a.lastBOSDir = -1
		}
	}

	// ── 4. Finalise OBs for events whose +2-bar look-ahead has closed ──
	// Pillar 2 in DetectOrderBlocks checks FVGs up to obIdx+3, and obIdx
	// can be as recent as e.Index-1, so the worst-case future-leak is to
	// FVGs at index e.Index+2. Once i >= e.Index+2 the window is stable
	// and we can move the event from pending into the finalised slice.
	threshold := i - 2
	j := 0
	for ; j < len(a.pendingEvents); j++ {
		e := a.pendingEvents[j]
		if e.Index > threshold {
			break
		}
		if ob, ok := a.detectOBForEvent(e); ok {
			a.orderBlocks = append(a.orderBlocks, ob)
		}
	}
	if j > 0 {
		a.pendingEvents = a.pendingEvents[j:]
	}
}

// detectOBForEvent mirrors the inner-loop body of DetectOrderBlocks for a
// single structure event. Kept inline-equivalent (down to iteration order
// and break conditions) so parity testing actually proves something.
func (a *IncrementalAnalyzer) detectOBForEvent(e StructureEvent) (OrderBlock, bool) {
	if e.Kind != StructBOSBull && e.Kind != StructBOSBear &&
		e.Kind != StructCHoChBull && e.Kind != StructCHoChBear {
		return OrderBlock{}, false
	}
	bull := e.Kind == StructBOSBull || e.Kind == StructCHoChBull

	obIdx := -1
	for j := e.Index - 1; j >= 0 && j > e.Index-20; j-- {
		c := a.candles[j]
		if bull && c.Close < c.Open {
			obIdx = j
			break
		}
		if !bull && c.Close > c.Open {
			obIdx = j
			break
		}
	}
	if obIdx < 0 {
		return OrderBlock{}, false
	}
	ob := a.candles[obIdx]

	// Pillar 1 — prior liquidity sweep within 10 candles before the OB.
	swept := false
	for k := obIdx - 1; k >= 0 && obIdx-k <= 10; k-- {
		for s := len(a.swings) - 1; s >= 0; s-- {
			sw := a.swings[s]
			if sw.Index >= k {
				continue
			}
			if k-sw.Index > 30 {
				break
			}
			if bull && !sw.IsHigh && a.candles[k].Low < sw.Price {
				swept = true
				break
			}
			if !bull && sw.IsHigh && a.candles[k].High > sw.Price {
				swept = true
				break
			}
		}
		if swept {
			break
		}
	}
	if !swept {
		return OrderBlock{}, false
	}

	// Pillar 2 — same-direction FVG within 3 candles after the OB.
	hasFVG := false
	for _, f := range a.fvgs {
		if f.Index <= obIdx {
			continue
		}
		if f.Index > obIdx+3 {
			break
		}
		if f.Bullish == bull {
			hasFVG = true
			break
		}
	}
	if !hasFVG {
		return OrderBlock{}, false
	}

	// Pillar 3 — no body-close beyond OB mean between OB and the event.
	mid := (ob.High + ob.Low) / 2
	for j := obIdx + 1; j < e.Index; j++ {
		if bull && a.candles[j].Close < mid {
			return OrderBlock{}, false
		}
		if !bull && a.candles[j].Close > mid {
			return OrderBlock{}, false
		}
	}

	return OrderBlock{Index: obIdx, ResolveIdx: e.Index, Bottom: ob.Low, Top: ob.High, Bullish: bull}, true
}
