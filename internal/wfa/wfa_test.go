package wfa

import (
	"testing"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
)

// sliceByTime is the seam that defines what data each IS/OOS window sees.
// A bug here (off-by-one, wrong half-openness) silently leaks future data
// into the in-sample weights — exactly the look-ahead bias the WFA design
// is supposed to rule out. These tests pin down the [from, to) contract.
func TestSliceByTime_HalfOpenBoundaries(t *testing.T) {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	candles := make([]core.Candle, 10)
	for i := range candles {
		candles[i].Time = base.Add(time.Duration(i) * time.Hour)
	}

	tests := []struct {
		name string
		from time.Time
		to   time.Time
		want []time.Time // expected candle times in the slice
	}{
		{
			"full range",
			base, base.Add(10 * time.Hour),
			times(base, 10),
		},
		{
			"exact boundary — from inclusive, to exclusive",
			base.Add(2 * time.Hour), base.Add(5 * time.Hour),
			times(base.Add(2*time.Hour), 3), // indices 2,3,4 — NOT 5
		},
		{
			"from before data",
			base.Add(-24 * time.Hour), base.Add(3 * time.Hour),
			times(base, 3),
		},
		{
			"to after data",
			base.Add(7 * time.Hour), base.Add(48 * time.Hour),
			times(base.Add(7*time.Hour), 3),
		},
		{
			"empty — from >= to",
			base.Add(5 * time.Hour), base.Add(5 * time.Hour),
			nil,
		},
		{
			"empty — to before data",
			base.Add(-10 * time.Hour), base.Add(-1 * time.Hour),
			nil,
		},
		{
			"empty — from after data",
			base.Add(20 * time.Hour), base.Add(30 * time.Hour),
			nil,
		},
		{
			"between candles — from at non-existent timestamp",
			base.Add(2*time.Hour + 30*time.Minute), base.Add(5*time.Hour + 30*time.Minute),
			// candles at hour 3, 4, 5 are inside [2:30, 5:30)
			times(base.Add(3*time.Hour), 3),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := sliceByTime(candles, tc.from, tc.to)
			if len(got) != len(tc.want) {
				t.Fatalf("len(got)=%d, want %d (got=%v)", len(got), len(tc.want), times2(got))
			}
			for i, w := range tc.want {
				if !got[i].Time.Equal(w) {
					t.Fatalf("got[%d].Time=%v, want %v", i, got[i].Time, w)
				}
			}
		})
	}
}

// Look-ahead guard: the slice [from, to) must never include the upper
// bound. If it did, the IS slice would contain the first OOS candle and
// the WFA promise of model/data separation would be broken.
func TestSliceByTime_NoLookAhead(t *testing.T) {
	base := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	candles := make([]core.Candle, 5)
	for i := range candles {
		candles[i].Time = base.Add(time.Duration(i) * time.Hour)
	}
	isEnd := base.Add(3 * time.Hour) // OOS starts here

	is := sliceByTime(candles, base, isEnd)
	if len(is) != 3 {
		t.Fatalf("IS should contain 3 candles (hours 0-2), got %d", len(is))
	}
	for _, c := range is {
		if !c.Time.Before(isEnd) {
			t.Fatalf("IS contains candle at %v which is not strictly < isEnd %v — look-ahead leak", c.Time, isEnd)
		}
	}
}

func times(start time.Time, n int) []time.Time {
	out := make([]time.Time, n)
	for i := range out {
		out[i] = start.Add(time.Duration(i) * time.Hour)
	}
	return out
}

func times2(cs []core.Candle) []time.Time {
	out := make([]time.Time, len(cs))
	for i, c := range cs {
		out[i] = c.Time
	}
	return out
}
