package meta

import (
	"math"
	"testing"
)

// Empty buffer: any percentile should return 0 with no panic.
func TestThresholdBuffer_EmptyReturnsZero(t *testing.T) {
	b := NewThresholdBuffer(100)
	for _, p := range []float64{0, 0.5, 0.9, 0.95, 1.0} {
		if got := b.Percentile(p); got != 0 {
			t.Fatalf("Percentile(%v) on empty buffer = %v, want 0", p, got)
		}
	}
}

// Single sample: every percentile collapses to that sample.
func TestThresholdBuffer_SingleSample(t *testing.T) {
	b := NewThresholdBuffer(100)
	b.Push(2.5)
	for _, p := range []float64{0, 0.5, 0.95, 1.0} {
		if got := b.Percentile(p); got != 2.5 {
			t.Fatalf("Percentile(%v) on single-sample buffer = %v, want 2.5", p, got)
		}
	}
}

// Known distribution: percentiles should match the nearest-rank
// expectation. With samples 1..100, P95 → 95, P50 → 50.
func TestThresholdBuffer_KnownDistribution(t *testing.T) {
	b := NewThresholdBuffer(100)
	for i := 1; i <= 100; i++ {
		b.Push(float64(i))
	}
	// idx = round(p × (n-1)) with n=100 → n-1=99, and the sorted slice
	// holds values 1..100 so the answer is idx+1.
	for _, p := range []float64{0, 0.25, 0.5, 0.95, 1} {
		want := float64(int(math.Round(p*99))) + 1
		if got := b.Percentile(p); got != want {
			t.Fatalf("Percentile(%v) = %v, want %v", p, got, want)
		}
	}
}

// Ring-buffer wraparound: pushing past capacity must evict the oldest
// samples and the percentile must reflect only the surviving window.
func TestThresholdBuffer_WrapsAroundCapacity(t *testing.T) {
	b := NewThresholdBuffer(5)
	// First 5 fill the buffer with 1..5.
	for i := 1; i <= 5; i++ {
		b.Push(float64(i))
	}
	if b.Len() != 5 {
		t.Fatalf("Len after fill = %d, want 5", b.Len())
	}
	// Push 100; should evict the 1 and now hold {2,3,4,5,100}.
	b.Push(100)
	if b.Len() != 5 {
		t.Fatalf("Len after wrap = %d, want 5", b.Len())
	}
	// Max (P=1) must now be 100; min (P=0) must be 2 (the 1 was evicted).
	if got := b.Percentile(1); got != 100 {
		t.Fatalf("Percentile(1) after wrap = %v, want 100", got)
	}
	if got := b.Percentile(0); got != 2 {
		t.Fatalf("Percentile(0) after wrap = %v, want 2", got)
	}
}

// Lazy sort: pushing then re-querying must reflect the new sample without
// the caller needing to call any rebuild method.
func TestThresholdBuffer_LazyResortAfterPush(t *testing.T) {
	b := NewThresholdBuffer(10)
	for i := 1; i <= 5; i++ {
		b.Push(float64(i))
	}
	if got := b.Percentile(1); got != 5 {
		t.Fatalf("Percentile(1) pre-push = %v, want 5", got)
	}
	b.Push(99)
	if got := b.Percentile(1); got != 99 {
		t.Fatalf("Percentile(1) post-push = %v, want 99 (lazy resort failed)", got)
	}
}

// Boundary conditions: P > 1 returns max, P < 0 returns min — same as
// numpy.percentile(method="nearest").
func TestThresholdBuffer_PercentileBoundaries(t *testing.T) {
	b := NewThresholdBuffer(10)
	for _, v := range []float64{3, 1, 4, 1, 5, 9, 2, 6, 5, 3} {
		b.Push(v)
	}
	if got := b.Percentile(-0.5); got != 1 {
		t.Fatalf("Percentile(<0) = %v, want min=1", got)
	}
	if got := b.Percentile(1.5); got != 9 {
		t.Fatalf("Percentile(>1) = %v, want max=9", got)
	}
}
