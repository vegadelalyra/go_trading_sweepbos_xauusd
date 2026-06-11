package strategy

import (
	"math"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
)

// DefaultRR is the seed reward:risk multiple. A strategy with a strong edge
// at lower RR (faster TPs, higher hit rate) can override on its own config.
const DefaultRR = 2.0

// DefaultSLBuffer is the dollar pad added beyond the structural SL anchor so
// the stop sits outside the immediate wick volatility. Override per-strategy
// when the natural noise band differs (e.g. tighter on H4, wider on M15
// during news).
const DefaultSLBuffer = 0.10

// MinRR is the floor every signal must clear. BuildSignal rejects anything
// below this regardless of the strategy's own RR choice — a strategy can
// aim higher but never lower than 1.5R.
const MinRR = 1.5

// PickSide turns a directional bool into the matching Side value.
func PickSide(bull bool) core.Side {
	if bull {
		return core.Long
	}
	return core.Short
}

// ValidRR enforces TP at least MinRR×|entry−SL| away. Without this, a wide-SL
// setup paired with a tight TP becomes a coin-flip masquerading as a trade.
func ValidRR(entry, sl, tp float64, bull bool) bool {
	if bull {
		return sl < entry && tp > entry && (tp-entry) >= MinRR*(entry-sl)
	}
	return sl > entry && tp < entry && (entry-tp) >= MinRR*(sl-entry)
}

// ValidSLDistance returns true if |entry−SL| is within maxPoints. A
// strategy-local guard for setups that know their own ceiling; the engine
// applies a global MaxSLPoints separately via BacktestParams.
func ValidSLDistance(entry, sl, maxPoints float64) bool {
	if maxPoints <= 0 {
		return true
	}
	return math.Abs(entry-sl) <= maxPoints
}

// TPFromRR derives the TP price for a given entry+SL using the supplied RR.
// Pass rr <= 0 to fall back to DefaultRR — keeps call sites terse for the
// common case while leaving custom RR available without a second helper.
func TPFromRR(entry, sl, rr float64, bull bool) float64 {
	if rr <= 0 {
		rr = DefaultRR
	}
	if bull {
		return entry + (entry-sl)*rr
	}
	return entry - (sl-entry)*rr
}

// BuildSignal centralises validation so each strategy only describes the
// "physics" of its setup (where to enter / where the structural SL lives).
// Returns (Signal{}, false) if any quality gate fails.
func BuildSignal(name string, bull bool, entry, sl, tp float64, idx int, t time.Time, reason string) (core.Signal, bool) {
	if !ValidRR(entry, sl, tp, bull) {
		return core.Signal{}, false
	}
	return core.Signal{
		Strategy: name,
		Side:     PickSide(bull),
		Entry:    entry,
		SL:       sl,
		TP:       tp,
		Index:    idx,
		Time:     t,
		Reason:   reason,
	}, true
}
