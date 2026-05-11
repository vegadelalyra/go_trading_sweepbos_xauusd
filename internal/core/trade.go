package core

import "time"

// Trade is the closed-out record the backtester produces. R is realised in
// risk-multiples (loss = -1, win = +RR), BalanceAfter is the equity right
// after the trade settles, and the YearMonth/YearQuarter integer keys are
// used by the bucket-style reports.
type Trade struct {
	Strategy      string
	Side          Side
	EntryIdx      int
	ExitIdx       int
	EntryTime     time.Time
	ExitTime      time.Time
	Entry         float64
	SL            float64
	TP            float64
	ExitPrice     float64
	R             float64
	Win           bool
	BalanceAfter  float64
	Session       string
	YearMonth     int
	YearQuarter   int
	CandlesToExit int
}
