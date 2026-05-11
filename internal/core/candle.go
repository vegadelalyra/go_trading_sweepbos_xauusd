// Package core holds the small, framework-free types that every other internal
// package depends on. Anything that would otherwise create an import cycle
// between strategies, analytics and the execution engine lives here.
package core

import "time"

// Candle is the OHLC bar everything else consumes.
type Candle struct {
	Time                   time.Time
	Open, High, Low, Close float64
}

// Side encodes trade direction. Long/Short are the only legal values; iota
// keeps the zero-value (Long) as a meaningful default for uninitialised Trades.
type Side int

const (
	Long Side = iota
	Short
)

func (s Side) String() string {
	if s == Long {
		return "LONG"
	}
	return "SHORT"
}
