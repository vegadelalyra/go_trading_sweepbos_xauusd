package main

// Blank-import every strategy family so their package init() functions run
// and each strategy registers itself with internal/strategy. Without these
// imports the registry stays empty and the engine never sees any strategy.
//
// xasset is already imported by cli.go (non-blank, for SetXAG), so it would
// register either way; keeping the blank import here documents the intent
// alongside the other two families.
import (
	_ "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/smc"
	_ "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/ta"
	_ "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/xasset"
)
