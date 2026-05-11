package main

// Bare-name aliases for the types and helpers that internal subpackages own.
// `package main` (engine, meta, wfa, cli, tui, report, db) keeps using these
// short names so the diff against the pre-refactor codebase stays small. The
// canonical definitions live in:
//
//   core      — Candle, Side, Signal, Trade, session helpers
//   smc       — Analysis (the SMC primitives output)
//   indicator — ATRAt (the only indicator main still calls directly)
//   bias      — BiasMap, BuildBiasMap
//
// Every strategy now lives in internal/strategy/{smc,ta,xasset} and reaches
// for these packages by their canonical paths — they do NOT depend on this
// file.

import (
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/bias"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/indicator"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
)

type (
	Candle   = core.Candle
	Side     = core.Side
	Signal   = core.Signal
	Trade    = core.Trade
	Analysis = smc.Analysis
	BiasMap  = bias.BiasMap
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
	toNY              = core.ToNY
	classifySession   = core.ClassifySession
	yearMonthBucket   = core.YearMonthBucket
	yearQuarterBucket = core.YearQuarterBucket

	Analyze      = smc.Analyze
	ATRAt        = indicator.ATRAt
	BuildBiasMap = bias.BuildBiasMap
)
