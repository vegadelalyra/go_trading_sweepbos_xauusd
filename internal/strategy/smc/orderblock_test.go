package smc

import (
	"testing"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
)

// approxEq is a tolerant float comparator — synthetic OHLC values are exact,
// but ATR(14) chains TR through Wilder smoothing so we leave 1e-9 slack.
func approxEq(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}

// Gate A — wick-dominant bullish OB: the rejection wick is 50% of the full
// range, comfortably above the 0.40 threshold, so the zone must collapse onto
// the lower wick [Low, min(Open,Close)] and the helper must report
// isRejection=true.
func TestRefineOBZone_WickDominantBullishUsesWick(t *testing.T) {
	// down-candle (Close < Open) with a long lower wick.
	// O=2000, H=2002, L=1990, C=1996 → full=12, body=4, lower wick=6 (50%).
	c := core.Candle{Open: 2000, High: 2002, Low: 1990, Close: 1996}

	bottom, top, isRej := refineOBZone(c, true, 0.40)

	if !isRej {
		t.Fatalf("expected rejection-wick zone, got body zone")
	}
	if !approxEq(bottom, 1990) || !approxEq(top, 1996) {
		t.Fatalf("wick zone wrong: want [1990, 1996], got [%v, %v]", bottom, top)
	}
}

// Gate A — body-dominant bearish OB: the rejection wick is only 16.7% of
// the full range, below the 0.40 threshold, so the zone must remain the
// body [Open, Close] (an up-candle's body) and the wicks must NOT be
// included. The helper must report isRejection=false.
func TestRefineOBZone_BodyDominantBearishUsesBody(t *testing.T) {
	// up-candle (Close > Open) with a small upper wick — body-dominant.
	// O=2000, H=2010, L=1998, C=2008 → full=12, body=8, upper wick=2 (16.7%).
	c := core.Candle{Open: 2000, High: 2010, Low: 1998, Close: 2008}

	bottom, top, isRej := refineOBZone(c, false, 0.40)

	if isRej {
		t.Fatalf("expected body zone, got rejection-wick zone")
	}
	// Body of an up-candle = [Open, Close] = [2000, 2008]. Wicks (1998, 2010)
	// must NOT be part of the zone.
	if !approxEq(bottom, 2000) || !approxEq(top, 2008) {
		t.Fatalf("body zone wrong: want [2000, 2008], got [%v, %v]", bottom, top)
	}
}

// Gate B — displacement rejection: the impulsive candle's body ratio is
// below 0.70 (a wickey bar), so hasValidDisplacement must return false even
// though the bar's full range comfortably exceeds 1.5×ATR.
func TestHasValidDisplacement_BodyRatioFails(t *testing.T) {
	candles := buildBaselineCandles(60, 2000)
	obIdx := 40

	// Replace candle obIdx+1 with a wickey "impulse": full range = 12 (large
	// vs ATR≈1), body = 3 (ratio 0.25, well below 0.70). Direction is bullish
	// (Close > Open) so it would pass direction but fail bodyRatio.
	candles[obIdx+1] = core.Candle{
		Time: candles[obIdx+1].Time,
		Open: 2000, High: 2009, Low: 1997, Close: 2003,
	}
	// Make obIdx+2 and obIdx+3 also weak so the only candidate is the wickey one.
	candles[obIdx+2] = core.Candle{
		Time: candles[obIdx+2].Time,
		Open: 2003, High: 2003.3, Low: 2002.7, Close: 2003.1,
	}
	candles[obIdx+3] = core.Candle{
		Time: candles[obIdx+3].Time,
		Open: 2003.1, High: 2003.4, Low: 2002.8, Close: 2003.2,
	}

	cfg := DefaultOrderBlockMitConfig()
	if hasValidDisplacement(candles, obIdx, true, cfg) {
		t.Fatalf("expected wickey impulse (bodyRatio<0.70) to be rejected, but it passed")
	}
}

// Gate B — displacement rejection: the candidate impulse satisfies bodyRatio
// but its full range is too small relative to ATR(14), so hasValidDisplacement
// must return false. The flat baseline gives ATR ≈ 1.0; a range of 1.2 is
// below the 1.5× threshold.
func TestHasValidDisplacement_BarSizeFails(t *testing.T) {
	candles := buildBaselineCandles(60, 2000)
	obIdx := 40

	// All three candidates are full-body bullish but tiny relative to ATR≈1.0.
	for j := 1; j <= 3; j++ {
		candles[obIdx+j] = core.Candle{
			Time: candles[obIdx+j].Time,
			Open: 2000, High: 2001.2, Low: 2000, Close: 2001.2,
		}
	}

	cfg := DefaultOrderBlockMitConfig()
	if hasValidDisplacement(candles, obIdx, true, cfg) {
		t.Fatalf("expected sub-ATR impulse (barSize<1.5×ATR) to be rejected, but it passed")
	}
}

// Gate B — happy path: an impulse with bodyRatio≥0.70 AND range≥1.5×ATR(14)
// must pass. Sanity-check that the strict gates are not pathologically
// rejecting every input.
func TestHasValidDisplacement_PassesOnRealImpulse(t *testing.T) {
	candles := buildBaselineCandles(60, 2000)
	obIdx := 40

	// Strong bullish impulse: O=2000, H=2005, L=2000, C=2004.5 → full=5,
	// body=4.5, ratio=0.90, and 5 > 1.5×ATR≈1.5.
	candles[obIdx+1] = core.Candle{
		Time: candles[obIdx+1].Time,
		Open: 2000, High: 2005, Low: 2000, Close: 2004.5,
	}

	cfg := DefaultOrderBlockMitConfig()
	if !hasValidDisplacement(candles, obIdx, true, cfg) {
		t.Fatalf("expected a clean bull impulse (90%% body, 5×range vs ATR≈1) to pass")
	}
}

// End-to-end Detect: an OB whose displacement bar fails Gate B must yield no
// signal even when every other condition (age, mitigation, inducement, in-zone
// touch) is satisfied. Confirms the gates wire through Detect, not just the
// helpers.
func TestDetect_DisplacementFailureSuppressesSignal(t *testing.T) {
	candles := buildBaselineCandles(80, 2000)
	obIdx := 40
	curIdx := 60 // age = 20, between MinAge=5 and MaxAge=50.

	// Bullish OB origin candle: down-candle with a dominant body (rejection
	// wick well below 0.40 so Gate A keeps the body).
	// O=2002, H=2002.3, L=1998, C=1998.5 → full=4.3, lower wick=0.5 (12%).
	candles[obIdx] = core.Candle{
		Time: candles[obIdx].Time,
		Open: 2002, High: 2002.3, Low: 1998, Close: 1998.5,
	}
	// Make the post-OB candles weak / soft so Gate B rejects.
	for j := 1; j <= 3; j++ {
		candles[obIdx+j] = core.Candle{
			Time: candles[obIdx+j].Time,
			Open: 2000, High: 2001.1, Low: 1999.9, Close: 2000.1,
		}
	}
	// Current candle touches the OB zone (body = [1998.5, 2002]).
	candles[curIdx] = core.Candle{
		Time: candles[curIdx].Time,
		Open: 2000.5, High: 2001.0, Low: 1999.0, Close: 2000.0,
	}

	// Hand-craft an Analysis carrying one bullish OB at obIdx with the
	// primitive's full-candle zone — the strategy refines it internally.
	a := prim.Analysis{
		OrderBlocks: []prim.OrderBlock{
			{
				Index:      obIdx,
				ResolveIdx: obIdx + 4, // resolved before curIdx
				Bottom:     1998,
				Top:        2002.3,
				Bullish:    true,
			},
		},
	}

	s := &OrderBlockMit{Cfg: DefaultOrderBlockMitConfig()}
	if _, ok := s.Detect(candles, a, curIdx); ok {
		t.Fatalf("expected no signal when displacement gate fails, but got one")
	}
}

// buildBaselineCandles produces n candles each with a tiny ~1$ true-range
// drift so ATR(14) settles around 1.0. Tests then surgically overwrite
// specific indices to set up the scenario under inspection.
func buildBaselineCandles(n int, base float64) []core.Candle {
	out := make([]core.Candle, n)
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	price := base
	for i := range out {
		// Alternating tiny up/down bars give a stable ATR≈1.0 without trend.
		var open, hi, lo, cl float64
		open = price
		if i%2 == 0 {
			cl = price + 0.5
			hi = cl + 0.3
			lo = open - 0.2
		} else {
			cl = price - 0.5
			lo = cl - 0.3
			hi = open + 0.2
		}
		out[i] = core.Candle{
			Time: start.Add(time.Duration(i) * 15 * time.Minute),
			Open: open, High: hi, Low: lo, Close: cl,
		}
		price = cl
	}
	return out
}
