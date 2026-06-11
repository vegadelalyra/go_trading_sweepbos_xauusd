package smc

import (
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
)

// syntheticCandles generates a deterministic OHLC walk that exercises every
// primitive: enough volatility to spawn frequent swings, occasional gaps
// large enough to imprint FVGs, and trending stretches that break structure.
// Seeded with a fixed value so the golden comparison is reproducible.
func syntheticCandles(n int, seed int64) []core.Candle {
	r := rand.New(rand.NewSource(seed))
	out := make([]core.Candle, n)
	price := 2000.0
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		drift := (r.Float64() - 0.5) * 6.0
		rangeSize := 0.5 + r.Float64()*4.0
		open := price
		close := price + drift
		high := open
		if close > high {
			high = close
		}
		high += r.Float64() * rangeSize
		low := open
		if close < low {
			low = close
		}
		low -= r.Float64() * rangeSize

		// Inject the occasional displacement so FVGs and OB pillars fire.
		if r.Intn(50) == 0 {
			jump := (r.Float64() - 0.5) * 30
			close += jump
			if close > high {
				high = close
			}
			if close < low {
				low = close
			}
		}

		out[i] = core.Candle{
			Time:  start.Add(time.Duration(i) * 5 * time.Minute),
			Open:  open,
			High:  high,
			Low:   low,
			Close: close,
		}
		price = close
	}
	return out
}

func assertAnalysisEqual(t *testing.T, want, got Analysis) {
	t.Helper()
	if !reflect.DeepEqual(want.Swings, got.Swings) {
		t.Errorf("Swings divergence: want %d entries, got %d entries", len(want.Swings), len(got.Swings))
		for i := range max(len(want.Swings), len(got.Swings)) {
			if i >= len(want.Swings) || i >= len(got.Swings) || want.Swings[i] != got.Swings[i] {
				var w, g any
				if i < len(want.Swings) {
					w = want.Swings[i]
				}
				if i < len(got.Swings) {
					g = got.Swings[i]
				}
				t.Errorf("  Swing[%d]: want %+v, got %+v", i, w, g)
				if i > 5 {
					break
				}
			}
		}
	}
	if !reflect.DeepEqual(want.FVGs, got.FVGs) {
		t.Errorf("FVGs divergence: want %d, got %d", len(want.FVGs), len(got.FVGs))
		for i := range max(len(want.FVGs), len(got.FVGs)) {
			if i >= len(want.FVGs) || i >= len(got.FVGs) || want.FVGs[i] != got.FVGs[i] {
				var w, g any
				if i < len(want.FVGs) {
					w = want.FVGs[i]
				}
				if i < len(got.FVGs) {
					g = got.FVGs[i]
				}
				t.Errorf("  FVG[%d]: want %+v, got %+v", i, w, g)
				if i > 5 {
					break
				}
			}
		}
	}
	if !reflect.DeepEqual(want.Structure, got.Structure) {
		t.Errorf("Structure divergence: want %d, got %d", len(want.Structure), len(got.Structure))
		for i := range max(len(want.Structure), len(got.Structure)) {
			if i >= len(want.Structure) || i >= len(got.Structure) || want.Structure[i] != got.Structure[i] {
				var w, g any
				if i < len(want.Structure) {
					w = want.Structure[i]
				}
				if i < len(got.Structure) {
					g = got.Structure[i]
				}
				t.Errorf("  StructureEvent[%d]: want %+v, got %+v", i, w, g)
				if i > 5 {
					break
				}
			}
		}
	}
	if !reflect.DeepEqual(want.OrderBlocks, got.OrderBlocks) {
		t.Errorf("OrderBlocks divergence: want %d, got %d", len(want.OrderBlocks), len(got.OrderBlocks))
		for i := range max(len(want.OrderBlocks), len(got.OrderBlocks)) {
			if i >= len(want.OrderBlocks) || i >= len(got.OrderBlocks) || want.OrderBlocks[i] != got.OrderBlocks[i] {
				var w, g any
				if i < len(want.OrderBlocks) {
					w = want.OrderBlocks[i]
				}
				if i < len(got.OrderBlocks) {
					g = got.OrderBlocks[i]
				}
				t.Errorf("  OrderBlock[%d]: want %+v, got %+v", i, w, g)
				if i > 5 {
					break
				}
			}
		}
	}
}

// TestIncrementalParity_Append feeds the analyzer one candle at a time via
// Append and verifies the resulting Analysis matches smc.Analyze exactly.
// This is the production path: paper-live calls Append on each new bar.
func TestIncrementalParity_Append(t *testing.T) {
	for _, n := range []int{50, 500, 5000, 20000} {
		for _, seed := range []int64{1, 42, 1337} {
			candles := syntheticCandles(n, seed)
			want := Analyze(candles)

			a := NewIncrementalAnalyzer()
			for _, c := range candles {
				a.Append(c)
			}
			got := a.Analysis()
			t.Run("Append", func(t *testing.T) {
				assertAnalysisEqual(t, want, got)
			})
			if t.Failed() {
				t.Fatalf("first parity failure at n=%d seed=%d", n, seed)
			}
		}
	}
}

// TestIncrementalParity_Reset checks the startup path: bulk-seed from a
// candle slice. Must produce the same Analysis as Analyze.
func TestIncrementalParity_Reset(t *testing.T) {
	for _, n := range []int{50, 500, 5000} {
		candles := syntheticCandles(n, 7)
		want := Analyze(candles)

		a := NewIncrementalAnalyzer()
		a.Reset(candles)
		got := a.Analysis()
		assertAnalysisEqual(t, want, got)
		if t.Failed() {
			t.Fatalf("Reset parity failure at n=%d", n)
		}
	}
}

// TestIncrementalParity_ResetThenAppend models the paper-live restart path:
// Reset from full history, then keep going with Append for new bars. The
// final Analysis after additional bars must equal Analyze(all-bars).
func TestIncrementalParity_ResetThenAppend(t *testing.T) {
	candles := syntheticCandles(10000, 99)
	split := 7500

	a := NewIncrementalAnalyzer()
	a.Reset(candles[:split])
	for _, c := range candles[split:] {
		a.Append(c)
	}
	got := a.Analysis()
	want := Analyze(candles)
	assertAnalysisEqual(t, want, got)
}

// TestIncrementalParity_DoubleReset proves Reset is idempotent: feeding the
// same slice twice in a row produces the same Analysis (slices are reset
// in place via [:0], guarding against accidental append-onto-stale state).
func TestIncrementalParity_DoubleReset(t *testing.T) {
	candles := syntheticCandles(2000, 11)
	a := NewIncrementalAnalyzer()
	a.Reset(candles)
	first := a.Analysis()
	firstCopy := Analysis{
		Swings:      append([]Swing(nil), first.Swings...),
		FVGs:        append([]FVG(nil), first.FVGs...),
		Structure:   append([]StructureEvent(nil), first.Structure...),
		OrderBlocks: append([]OrderBlock(nil), first.OrderBlocks...),
	}
	a.Reset(candles)
	second := a.Analysis()
	assertAnalysisEqual(t, firstCopy, second)
}

// BenchmarkAnalyzeFull measures the batch baseline — what paper-live runs
// today on every poll.
func BenchmarkAnalyzeFull(b *testing.B) {
	candles := syntheticCandles(165000, 1)
	b.ResetTimer()
	for b.Loop() {
		_ = Analyze(candles)
	}
}

// BenchmarkIncrementalAppendOne measures the per-bar Append cost on top of
// a fully-seeded analyzer — the latency that actually matters in paper-live.
// Reset cost is excluded from b.N because it's amortised across a session.
func BenchmarkIncrementalAppendOne(b *testing.B) {
	candles := syntheticCandles(165000, 1)
	a := NewIncrementalAnalyzer()
	a.Reset(candles[:len(candles)-1])
	last := candles[len(candles)-1]
	b.ResetTimer()
	for b.Loop() {
		// Snapshot length so each iteration appends-and-rolls-back to the
		// same state, keeping the benchmark steady.
		preLen := len(a.candles)
		preSwings := len(a.swings)
		preFVGs := len(a.fvgs)
		preStruct := len(a.structure)
		preOBs := len(a.orderBlocks)
		preDir := a.lastBOSDir
		a.Append(last)
		a.candles = a.candles[:preLen]
		a.swings = a.swings[:preSwings]
		a.fvgs = a.fvgs[:preFVGs]
		a.structure = a.structure[:preStruct]
		a.orderBlocks = a.orderBlocks[:preOBs]
		a.lastBOSDir = preDir
	}
}

// BenchmarkIncrementalResetFull measures the cost of building the analyzer
// from scratch on 165k candles. This is the cold-start / fallback path.
func BenchmarkIncrementalResetFull(b *testing.B) {
	candles := syntheticCandles(165000, 1)
	a := NewIncrementalAnalyzer()
	b.ResetTimer()
	for b.Loop() {
		a.Reset(candles)
	}
}
