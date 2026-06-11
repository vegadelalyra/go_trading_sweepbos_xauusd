package meta

import (
	"math"
	"sort"
)

// DefaultThresholdBufferCap is the ring-buffer capacity used by paper-live
// and WFA when no explicit cap is supplied. 2000 candles ≈ 1 trading week
// on 5m, ≈ 3 trading weeks on 15m — enough samples for a stable upper-tail
// percentile, short enough to track regime drift.
const DefaultThresholdBufferCap = 2000

// ThresholdBuffer holds a fixed-capacity rolling sample of |NetWeight|
// observations from ComposeSignal. It powers the dynamic-percentile
// thresholding path: instead of comparing the candle's net weight against
// a hard-coded 2.0, the caller compares it against a P95 (or any other
// upper-tail percentile) of the most recent N observations, so the gate
// auto-regulates as the active-signal distribution moves with the regime.
//
// Only "active" candles (where total weight > 0, i.e. at least one
// strategy fired) are pushed. Pushing zeros every quiet candle would drag
// the percentile to zero in long flat stretches and defeat the purpose.
//
// Not safe for concurrent use; paper-live's single goroutine and WFA's
// per-window scope are both single-threaded.
type ThresholdBuffer struct {
	data  []float64
	cap   int
	idx   int  // next write position
	count int  // total observations seen, capped at cap
	dirty bool // true between writes; ScratchSort lazily rebuilds the sorted copy
	sorted []float64
}

// NewThresholdBuffer returns a buffer of the given capacity. cap ≤ 0
// falls back to DefaultThresholdBufferCap.
func NewThresholdBuffer(cap int) *ThresholdBuffer {
	if cap <= 0 {
		cap = DefaultThresholdBufferCap
	}
	return &ThresholdBuffer{data: make([]float64, cap), cap: cap}
}

// Push appends a sample, evicting the oldest when full.
func (b *ThresholdBuffer) Push(v float64) {
	b.data[b.idx] = v
	b.idx = (b.idx + 1) % b.cap
	if b.count < b.cap {
		b.count++
	}
	b.dirty = true
}

// Len returns the number of valid samples currently held.
func (b *ThresholdBuffer) Len() int { return b.count }

// Percentile returns the value at the given fraction p ∈ [0,1] of the
// buffer's empirical distribution using nearest-rank with linear ordering
// (idx = round(p·(n-1))). Returns 0 when the buffer is empty.
//
// p ≥ 1 returns the maximum, p ≤ 0 returns the minimum — same convention
// as numpy.percentile with method="nearest".
func (b *ThresholdBuffer) Percentile(p float64) float64 {
	if b.count == 0 {
		return 0
	}
	if b.dirty {
		if cap(b.sorted) < b.count {
			b.sorted = make([]float64, b.count)
		} else {
			b.sorted = b.sorted[:b.count]
		}
		copy(b.sorted, b.data[:b.count])
		sort.Float64s(b.sorted)
		b.dirty = false
	}
	if p >= 1 {
		return b.sorted[b.count-1]
	}
	if p <= 0 {
		return b.sorted[0]
	}
	idx := int(math.Round(p * float64(b.count-1)))
	idx = max(idx, 0)
	if idx >= b.count {
		idx = b.count - 1
	}
	return b.sorted[idx]
}
