package main

import (
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// StrategyFn is the legacy function-style contract every in-main strategy
// still implements. Strategies that migrate to internal/strategy/* expose
// the same behaviour via the strategy.Strategy interface; once every legacy
// function is gone, this type and the Strategies slice below disappear with it.
type StrategyFn func(candles []Candle, a Analysis, i int) (Signal, bool)

// Strategies is the canonical list run by the backtest engine. Order is the
// priority order in which the engine evaluates signals (first match wins per
// candle). New strategies should register themselves through
// internal/strategy.Register from their family sub-package rather than being
// added here — this slice is a transitional bridge that A.8 retires.
var Strategies = []StrategyFn{
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
	EMACross,
	BollingerReversal,
	DailyBreakout,
	XAUXAGPairs, // inactive until XAGCandles is populated.
}

// Bare-name shims for legacy strategies that still live in `package main`.
// Each just defers to internal/strategy's canonical helper. As strategies
// migrate to internal/strategy/* they call strategy.BuildSignal etc.
// directly and these wrappers go with the rest of the alias layer.

const (
	fixedRR  = strategy.DefaultRR
	slBuffer = strategy.DefaultSLBuffer
)

func tpFromRR(entry, sl float64, bull bool) float64 {
	return strategy.TPFromRR(entry, sl, 0, bull)
}

func buildSignal(name string, bull bool, entry, sl, tp float64, idx int, t time.Time, reason string) (Signal, bool) {
	return strategy.BuildSignal(name, bull, entry, sl, tp, idx, t, reason)
}
