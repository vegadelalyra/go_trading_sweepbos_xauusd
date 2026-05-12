// Package strategy defines the contract every entry-signal generator
// implements, the family taxonomy used by reports and weight aggregation,
// and the shared helpers (BuildSignal / ValidRR / TPFromRR) every strategy
// reaches for. Concrete strategies live in family subpackages
// (internal/strategy/smc, /ta, /xasset) and register themselves on init.
//
// Design principle: each strategy owns its own config struct — RR, SL
// buffer, lookback windows, thresholds. The package-level constants here
// are seed defaults only; a strategy is free to override any of them.
// This is the "no contamination" guarantee — touching one strategy's
// parameters never reaches into another's.
package strategy

import (
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
)

// Family groups strategies by alpha source. Used by reports, the composite
// weight aggregator (per-family pooling), and the CLI to enable/disable
// whole families at once.
type Family string

const (
	FamilySMC    Family = "smc"
	FamilyTA     Family = "ta"
	FamilyXAsset Family = "xasset"
)

// Meta is the static description of a strategy. Mutable parameters belong on
// the strategy's own config struct; Meta is what the rest of the system uses
// to refer to the strategy by name and group it for reporting.
type Meta struct {
	Name        string
	Family      Family
	Description string
}

// Strategy is what every entry generator implements. Detect must be
// side-effect-free with respect to the inputs — the engine calls it for
// every candle and depends on identical inputs producing identical outputs.
type Strategy interface {
	Meta() Meta
	Detect(candles []core.Candle, a smc.Analysis, i int) (core.Signal, bool)
}

// IsBreakout reports whether a strategy is a breakout/momentum setup by
// name. Breakout strategies fire when price breaks ABOVE recent range
// highs (long) or BELOW recent range lows (short) — their entries land
// in the upper/lower extreme of the dealing range BY CONSTRUCTION, not
// by accident. Applying the SMC Premium/Discount filter to them is a
// category error: the filter encodes the mean-reversion thesis ("buy
// value, sell extreme"), the opposite of what a breakout sets up to do.
//
// Engine.RunBacktest, wfa.SimulateOOS and paper.acceptSignal all consult
// this to skip the PD gate for breakout signals. Mean-reversion-flavored
// strategies (FVGFill, OrderBlockMit, SilverBullet, OTE…) still get the
// PD filter applied — that's where the filter pays its keep.
func IsBreakout(name string) bool {
	switch name {
	case "DailyBreakout", "AsianRangeBO":
		return true
	}
	return false
}

// IsLondonFixMomentum reports whether a strategy is a momentum/breakout
// setup that should be PENALISED inside the London Gold Fix window
// (14:40-15:20 UTC). The intuition is identical to IsBreakout but the
// list also includes BreakerBlock — a continuation setup that, while not
// a pure breakout, still bets on directional follow-through, exactly the
// regime the 15:00 GMT fix manipulation disrupts.
//
// Distinct from IsBreakout: that predicate gates the SMC Premium/Discount
// filter (structural — only true breakouts qualify); this one gates the
// London Fix weight adjustment (behavioral — any "fade me at extremes"
// setup belongs here).
func IsLondonFixMomentum(name string) bool {
	switch name {
	case "DailyBreakout", "AsianRangeBO", "BreakerBlock":
		return true
	}
	return false
}

// IsLiquidityFade reports whether a strategy is a liquidity-fade setup
// designed to profit from the Judas-swing / stop-hunt → mean-reversion
// pattern. These strategies BENEFIT from the manipulation that hurts
// momentum plays around the 15:00 GMT London Gold Fix, so the London Fix
// filter boosts their weight inside the window rather than penalising it.
//
// The list mirrors the four "smart-money fade" strategies in the catalog:
// JudasSwing (the textbook fade), TurtleSoup (Larry Williams' fade of
// false breakouts), LiquiditySweepBoS (sweep-then-BoS), and SilverBullet
// (the 10-11 NY hour scalp that often catches the fix-window snap-back).
func IsLiquidityFade(name string) bool {
	switch name {
	case "JudasSwing", "TurtleSoup", "LiquiditySweepBoS", "SilverBullet":
		return true
	}
	return false
}
