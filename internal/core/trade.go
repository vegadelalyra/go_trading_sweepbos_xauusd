package core

import "time"

// Trade is the closed-out record the backtester produces. R is realised in
// risk-multiples (loss = -1, win = +RR), BalanceAfter is the equity right
// after the trade settles, and the YearMonth/YearQuarter integer keys are
// used by the bucket-style reports.
//
// When the triple-barrier exit is enabled (engine.TripleBarrierConfig),
// PartialR / PartialExit* are populated for the half-size partial-TP leg
// that fires at +PartialR_target; Trade.R then carries the weighted
// average of the partial leg (50%) and the final leg (50%), so downstream
// consumers (balance update, meta.LoadWeights) keep treating Trade.R as
// the full-position realised R-multiple. PartialR == 0 means no partial
// fired (legacy behaviour or partial-not-yet-triggered).
type Trade struct {
	Strategy         string
	Side             Side
	EntryIdx         int
	ExitIdx          int
	EntryTime        time.Time
	ExitTime         time.Time
	Entry            float64
	SL               float64
	TP               float64
	ExitPrice        float64
	R                float64
	Win              bool
	BalanceAfter     float64
	Session          string
	YearMonth        int
	YearQuarter      int
	CandlesToExit    int
	PartialR         float64   // R contributed by the partial leg; 0 when no partial fired
	PartialExitIdx   int       // bar index where the partial leg closed; 0 when no partial
	PartialExitTime  time.Time // wallclock of the partial leg close
	PartialExitPrice float64   // fill price of the partial leg
}
