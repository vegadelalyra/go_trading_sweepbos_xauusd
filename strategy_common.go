package main

import (
	"math"
	"time"
)

// StrategyFn is the contract every strategy implements. Returning
// (Signal, false) means "no setup right now"; the engine moves on.
type StrategyFn func(candles []Candle, a Analysis, i int) (Signal, bool)

// Strategies is the canonical list run by the backtest engine. Adding
// a new strategy means importing it here. Order is the priority order
// in which the engine evaluates signals (first match wins per candle).
var Strategies = []StrategyFn{
	// SMC / ICT family — order-flow-aware setups.
	SilverBullet,
	TurtleSoup,
	PowerOfThree,
	OrderBlockMitigation,
	FVGFill,
	BreakerBlock,
	LiquiditySweepBoS,
	OTEFibonacci,
	AsianRangeBreakout,
	JudasSwing,
	// Non-SMC / classical TA — diversify the alpha source.
	EMACross,
	BollingerReversal,
	DailyBreakout,
	// Cross-asset.
	XAUXAGPairs, // inactive until XAGCandles is populated; engine sees zero hits otherwise.
}

// ── Shared constants ────────────────────────────────────────────────────────

// fixedRR is the reward:risk multiple every strategy aims for by default.
// The engine will reject any signal whose TP doesn't satisfy validRR.
const fixedRR = 2.0

// slBuffer is the dollar pad added beyond the structural SL anchor (last
// swing, OB extreme, sweep wick…) so the stop sits outside the immediate
// wick volatility.
const slBuffer = 0.10

// ── Validation helpers ──────────────────────────────────────────────────────

func pickSide(bull bool) Side {
	if bull {
		return Long
	}
	return Short
}

// validRR enforces TP at least 1.5×|entry−SL| away. Without this, a wide-SL
// setup paired with a tight TP becomes a coin-flip masquerading as a trade.
func validRR(entry, sl, tp float64, bull bool) bool {
	if bull {
		return sl < entry && tp > entry && (tp-entry) >= 1.5*(entry-sl)
	}
	return sl > entry && tp < entry && (entry-tp) >= 1.5*(sl-entry)
}

// validSLDistance returns true if |entry−SL| is within maxPoints. This is
// a strategy-local guard for setups that already know their own ceiling
// (the engine also applies a global MaxSLPoints via BacktestParams).
func validSLDistance(entry, sl, maxPoints float64) bool {
	if maxPoints <= 0 {
		return true
	}
	return math.Abs(entry-sl) <= maxPoints
}

// ── Common signal builder ───────────────────────────────────────────────────

// buildSignal centralises validation so each strategy only describes the
// "physics" of its setup (where to enter / where structural SL lives).
// Returns (Signal{}, false) if any quality gate fails.
func buildSignal(name string, bull bool, entry, sl, tp float64, idx int, t time.Time, reason string) (Signal, bool) {
	if !validRR(entry, sl, tp, bull) {
		return Signal{}, false
	}
	return Signal{
		Strategy: name,
		Side:     pickSide(bull),
		Entry:    entry,
		SL:       sl,
		TP:       tp,
		Index:    idx,
		Time:     t,
		Reason:   reason,
	}, true
}

// tpFromRR derives the TP price for a given entry+SL using the fixed RR.
func tpFromRR(entry, sl float64, bull bool) float64 {
	if bull {
		return entry + (entry-sl)*fixedRR
	}
	return entry - (sl-entry)*fixedRR
}
