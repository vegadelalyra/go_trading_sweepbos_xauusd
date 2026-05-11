package main

// Transitional aliases that let `package main` keep using bare names
// (Candle, Side, Long, classifySession…) while the canonical definitions
// have moved into internal subpackages. Files that get migrated into the
// new packages (e.g. internal/strategy/smc) use the qualified names
// directly; everything still in main goes through this file.
//
// Delete this file once main no longer holds any code that needs these
// bare names — that closes the refactor.

import "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"

type (
	Candle = core.Candle
	Side   = core.Side
	Signal = core.Signal
	Trade  = core.Trade
)

const (
	Long  = core.Long
	Short = core.Short

	SessionAsia   = core.SessionAsia
	SessionLondon = core.SessionLondon
	SessionNYAM   = core.SessionNYAM
	SessionNYPM   = core.SessionNYPM
	SessionOff    = core.SessionOff
)

var (
	nyLoc             = core.NyLoc
	toNY              = core.ToNY
	classifySession   = core.ClassifySession
	yearMonthBucket   = core.YearMonthBucket
	yearQuarterBucket = core.YearQuarterBucket
)
