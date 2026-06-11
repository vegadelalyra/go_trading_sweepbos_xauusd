package engine

import (
	"math"
	"testing"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// stubStrategy fires once at a fixed index with fixed levels. Lets us
// drive the engine's lifecycle (entry → SL/TP/time-stop → exit accounting)
// in isolation, without coupling the test to any real strategy's logic.
type stubStrategy struct {
	fireAt int
	side   core.Side
	entry  float64
	sl, tp float64
}

func (s stubStrategy) Meta() strategy.Meta {
	return strategy.Meta{Name: "Stub", Family: strategy.FamilySMC}
}

func (s stubStrategy) Detect(_ []core.Candle, _ smc.Analysis, i int) (core.Signal, bool) {
	if i != s.fireAt {
		return core.Signal{}, false
	}
	return core.Signal{
		Strategy: "Stub", Side: s.side,
		Entry: s.entry, SL: s.sl, TP: s.tp,
	}, true
}

// makeCandles builds a synthetic OHLC slice flat at price `base` except
// for `hitIdx`, where High/Low are stretched to `hit` so the engine sees
// the target touched on exactly that candle.
func makeCandles(n int, base, hit float64, hitIdx int, longHit bool) []core.Candle {
	out := make([]core.Candle, n)
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range out {
		out[i] = core.Candle{
			Time:  start.Add(time.Duration(i) * 15 * time.Minute),
			Open:  base, High: base, Low: base, Close: base,
		}
	}
	if hitIdx >= 0 && hitIdx < n {
		c := &out[hitIdx]
		if longHit {
			c.High = hit
		} else {
			c.Low = hit
		}
	}
	return out
}

// TP hit: an obvious LONG that wins exactly 1.5×RR. The engine should
// book R = (TP-Entry)/(Entry-SL), mark Win=true, and exit on the candle
// whose High crosses TP. Spread is zeroed here so the assertion stays
// on the engine's RR math, not on spread accounting.
func TestRunBacktest_TPHitBooksRRWin(t *testing.T) {
	const base = 2000.0
	// LONG: entry 2000, SL 1990 (10 pts risk), TP 2015 (15 pts reward) → 1.5R.
	stub := stubStrategy{fireAt: 60, side: core.Long, entry: base, sl: 1990, tp: 2015}
	candles := makeCandles(120, base, 2015, 61, true) // TP touched one candle later

	p := DefaultParams()
	p.SpreadDollar = 0
	p.MinIndex = 50
	p.MaxSLPoints = 200
	res := RunBacktest(candles, []strategy.Strategy{stub}, p)

	if len(res.Trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(res.Trades))
	}
	tr := res.Trades[0]
	if !tr.Win {
		t.Fatalf("expected Win=true")
	}
	if math.Abs(tr.R-1.5) > 1e-9 {
		t.Fatalf("R=%v, want 1.5", tr.R)
	}
	if tr.EntryIdx != 60 || tr.ExitIdx != 61 {
		t.Fatalf("entryIdx=%d exitIdx=%d, want 60/61", tr.EntryIdx, tr.ExitIdx)
	}
}

// Spread accounting: a winning trade with non-zero spread must yield a
// strictly lower R than the same trade run with zero spread, because the
// effective entry is shifted by SpreadDollar against the trader. This
// pins the spread contract without locking the test to a magic number.
func TestRunBacktest_SpreadShiftsRRDown(t *testing.T) {
	const base = 2000.0
	stub := stubStrategy{fireAt: 60, side: core.Long, entry: base, sl: 1990, tp: 2015}
	candles := makeCandles(120, base, 2015, 61, true)

	p := DefaultParams()
	p.MinIndex = 50
	p.MaxSLPoints = 200

	p.SpreadDollar = 0
	zero := RunBacktest(candles, []strategy.Strategy{stub}, p)
	p.SpreadDollar = 0.5
	withSpread := RunBacktest(candles, []strategy.Strategy{stub}, p)

	if len(zero.Trades) != 1 || len(withSpread.Trades) != 1 {
		t.Fatalf("expected 1 trade each")
	}
	if !(withSpread.Trades[0].R < zero.Trades[0].R) {
		t.Fatalf("spread did not penalise R: zero=%v withSpread=%v",
			zero.Trades[0].R, withSpread.Trades[0].R)
	}
}

// SL hit: clean -1R fill when SlippagePoints=0 AND SpreadDollar=0. This
// is the canonical "loss = -1R" convention every R-based metric assumes.
func TestRunBacktest_SLHitBooksMinusOneR(t *testing.T) {
	const base = 2000.0
	stub := stubStrategy{fireAt: 60, side: core.Long, entry: base, sl: 1990, tp: 2015}
	candles := makeCandles(120, base, 1990, 61, false) // SL touched

	p := DefaultParams()
	p.SpreadDollar = 0
	p.MinIndex = 50
	p.MaxSLPoints = 200
	res := RunBacktest(candles, []strategy.Strategy{stub}, p)

	if len(res.Trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(res.Trades))
	}
	tr := res.Trades[0]
	if tr.Win {
		t.Fatalf("expected Win=false")
	}
	if math.Abs(tr.R-(-1)) > 1e-9 {
		t.Fatalf("R=%v, want -1 (no slippage, no spread)", tr.R)
	}
}

// MaxSLPoints rejection: a signal whose SL distance exceeds the cap must
// be skipped silently. Past bugs sized positions on >50pt SLs at 15m,
// which is a regime the strategies weren't designed for.
func TestRunBacktest_MaxSLPointsRejectsWideSL(t *testing.T) {
	const base = 2000.0
	// SL distance = 100 pts, cap = 50 → must reject.
	stub := stubStrategy{fireAt: 60, side: core.Long, entry: base, sl: 1900, tp: 2150}
	candles := makeCandles(120, base, 2150, 61, true)

	p := DefaultParams()
	p.MinIndex = 50
	p.MaxSLPoints = 50
	res := RunBacktest(candles, []strategy.Strategy{stub}, p)

	if len(res.Trades) != 0 {
		t.Fatalf("expected 0 trades (SL exceeds cap), got %d", len(res.Trades))
	}
}

// Time-stop: if neither SL nor TP touches before MaxHoldCandles, the
// position must close at the candle's close at the time-stop boundary.
func TestRunBacktest_TimeStopExitsAtClose(t *testing.T) {
	const base = 2000.0
	stub := stubStrategy{fireAt: 60, side: core.Long, entry: base, sl: 1990, tp: 2015}
	// No SL/TP touch anywhere — flat price for the entire run.
	candles := makeCandles(200, base, base, -1, true)

	p := DefaultParams()
	p.MinIndex = 50
	p.MaxHoldCandles = 8
	p.MaxSLPoints = 200
	res := RunBacktest(candles, []strategy.Strategy{stub}, p)

	if len(res.Trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(res.Trades))
	}
	tr := res.Trades[0]
	if tr.ExitIdx-tr.EntryIdx != p.MaxHoldCandles {
		t.Fatalf("time-stop fired at offset %d, want %d", tr.ExitIdx-tr.EntryIdx, p.MaxHoldCandles)
	}
	// Flat price → exit at entry (modulo spread); R should be ~0.
	if math.Abs(tr.R) > 0.1 {
		t.Fatalf("R=%v on flat-price time-stop, want ≈0", tr.R)
	}
}

// ComputeStats determinism: same trades, same params → same stats. The
// reported headline numbers must be a pure function of the inputs.
func TestComputeStats_Deterministic(t *testing.T) {
	trades := []core.Trade{
		{R: 1.5, Win: true, EntryTime: time.Now(), ExitTime: time.Now().Add(time.Hour)},
		{R: -1.0, Win: false, EntryTime: time.Now().Add(2 * time.Hour), ExitTime: time.Now().Add(3 * time.Hour)},
		{R: 1.5, Win: true, EntryTime: time.Now().Add(4 * time.Hour), ExitTime: time.Now().Add(5 * time.Hour)},
	}
	p := DefaultParams()
	s1 := ComputeStats(trades, p, 0.05, p.StartBalance*1.02)
	s2 := ComputeStats(trades, p, 0.05, p.StartBalance*1.02)

	if s1.NetR != s2.NetR || s1.WinRate != s2.WinRate || s1.Sharpe != s2.Sharpe {
		t.Fatalf("ComputeStats not deterministic: %+v vs %+v", s1, s2)
	}
	if s1.Total != 3 || s1.Wins != 2 || s1.Losses != 1 {
		t.Fatalf("Total/Wins/Losses=%d/%d/%d, want 3/2/1", s1.Total, s1.Wins, s1.Losses)
	}
	if math.Abs(s1.NetR-2.0) > 1e-9 {
		t.Fatalf("NetR=%v, want 2.0", s1.NetR)
	}
}

// AdvanceTrade is the single source of truth for the intra-position
// lifecycle (engine/wfa/paper all delegate). These tests pin down both
// the legacy path (TripleBarrier disabled — must reproduce the
// historical -1R/+RR/MaxHold behavior) and the triple-barrier overlay.

// Legacy SHORT TP: SL at 110, TP at 90, entry at 100 → c.Low ≤ 90 on a
// bar should produce R = (100-90)/(110-100) = +1.0R cleanly.
func TestAdvanceTrade_LegacyShortTP(t *testing.T) {
	p := DefaultParams()
	p.SpreadDollar = 0
	open := &core.Trade{
		Side: core.Short, EntryIdx: 0, Entry: 100, SL: 110, TP: 90,
	}
	c := core.Candle{Time: time.Now(), Open: 95, High: 95, Low: 89, Close: 90}
	if !AdvanceTrade(open, c, 5, p) {
		t.Fatal("expected close on TP touch")
	}
	if math.Abs(open.R-1.0) > 1e-9 {
		t.Fatalf("R=%v, want +1", open.R)
	}
	if !open.Win {
		t.Fatal("expected Win on TP")
	}
	if open.PartialR != 0 {
		t.Fatalf("legacy run set PartialR=%v, want 0", open.PartialR)
	}
}

// Legacy LONG SL: clean -1R when no slippage modelled, ExitPrice = SL - spread.
func TestAdvanceTrade_LegacyLongSL(t *testing.T) {
	p := DefaultParams()
	p.SpreadDollar = 0.5
	open := &core.Trade{
		Side: core.Long, EntryIdx: 0, Entry: 100, SL: 98, TP: 104,
	}
	c := core.Candle{Time: time.Now(), Open: 99, High: 99, Low: 97, Close: 97.5}
	if !AdvanceTrade(open, c, 3, p) {
		t.Fatal("expected close on SL touch")
	}
	if math.Abs(open.R-(-1)) > 1e-9 {
		t.Fatalf("R=%v, want -1", open.R)
	}
	if math.Abs(open.ExitPrice-(98-0.5)) > 1e-9 {
		t.Fatalf("ExitPrice=%v, want 97.5", open.ExitPrice)
	}
}

// Triple-barrier partial-TP only: a bar that just touches +1.5R triggers
// the partial leg but does NOT close the trade. SL must move to entry.
func TestAdvanceTrade_TBPartialFiresMovesSLToBE(t *testing.T) {
	p := DefaultParams()
	p.SpreadDollar = 0
	p.TripleBarrier = TripleBarrierConfig{
		Enabled: true, PartialR: 1.5, TimeStopCandles: 8, TimeStopRangeR: 0.3,
	}
	open := &core.Trade{
		Side: core.Long, EntryIdx: 0, Entry: 100, SL: 98, TP: 104,
	}
	// riskDist=2, partial target=100+1.5*2=103. High=103, low stays above entry.
	c := core.Candle{Time: time.Now(), Open: 101, High: 103, Low: 101, Close: 102}
	closed := AdvanceTrade(open, c, 4, p)
	if closed {
		t.Fatal("partial fired alone — trade should NOT be closed")
	}
	if math.Abs(open.PartialR-1.5) > 1e-9 {
		t.Fatalf("PartialR=%v, want +1.5", open.PartialR)
	}
	if open.SL != 100 {
		t.Fatalf("SL=%v, want 100 (BE)", open.SL)
	}
}

// After partial fires, an SL touch at BE produces R=0 on the final leg
// and Trade.R = 0.5*1.5 + 0.5*0 = +0.75.
func TestAdvanceTrade_TBPartialThenBEHitYieldsWeightedR(t *testing.T) {
	p := DefaultParams()
	p.SpreadDollar = 0
	p.TripleBarrier = TripleBarrierConfig{
		Enabled: true, PartialR: 1.5, TimeStopCandles: 8, TimeStopRangeR: 0.3,
	}
	open := &core.Trade{
		Side: core.Long, EntryIdx: 0, Entry: 100, SL: 98, TP: 104,
	}
	// Bar 1: partial fires (high=103).
	c1 := core.Candle{Time: time.Now(), Open: 101, High: 103, Low: 101, Close: 102}
	if AdvanceTrade(open, c1, 4, p) {
		t.Fatal("partial alone should not close trade")
	}
	// Bar 2: SL_BE (=100) hit.
	c2 := core.Candle{Time: time.Now(), Open: 100, High: 100, Low: 99, Close: 99.5}
	if !AdvanceTrade(open, c2, 5, p) {
		t.Fatal("BE hit on remaining leg should close trade")
	}
	if math.Abs(open.R-0.75) > 1e-9 {
		t.Fatalf("R=%v, want +0.75 (= (1.5+0)/2)", open.R)
	}
	if !open.Win {
		t.Fatal("weighted R is positive — expected Win=true")
	}
}

// Dynamic time-stop: after TimeStopCandles bars, if |unrealised R| ≤
// TimeStopRangeR, close at market.
func TestAdvanceTrade_TBDynamicTimeStopOnStagnation(t *testing.T) {
	p := DefaultParams()
	p.SpreadDollar = 0
	p.TripleBarrier = TripleBarrierConfig{
		Enabled: true, PartialR: 1.5, TimeStopCandles: 3, TimeStopRangeR: 0.3,
	}
	open := &core.Trade{
		Side: core.Long, EntryIdx: 0, Entry: 100, SL: 98, TP: 104,
	}
	// Two flat bars, neither partial nor SL/TP triggers.
	flat := core.Candle{Time: time.Now(), Open: 100.1, High: 100.1, Low: 99.9, Close: 100.05}
	if AdvanceTrade(open, flat, 1, p) {
		t.Fatal("flat bar at i=1 should not close")
	}
	if AdvanceTrade(open, flat, 2, p) {
		t.Fatal("flat bar at i=2 should not close")
	}
	// i=3 == EntryIdx+3 == TimeStopCandles → eligible. Close=100.05 is
	// 0.025R above entry (riskDist=2), well within 0.3R band → close.
	if !AdvanceTrade(open, flat, 3, p) {
		t.Fatal("stagnating bar at i=3 must trigger dynamic time-stop")
	}
	if math.Abs(open.R-0.025) > 1e-6 {
		t.Fatalf("R=%v, want ~0.025 (close=100.05 vs entry=100 / risk=2)", open.R)
	}
}
