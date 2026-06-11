#!/usr/bin/env bash
# Production launcher with Go 1.25 experimental features.
#
# Why these flags:
#   GOEXPERIMENT=greenteagc   The newer garbage collector lowers tail
#                             latency on the candle-by-candle hot loops
#                             (RunBacktest, simulateOOS). Off by default
#                             in 1.25; opt-in here for the prod binary.
#   GOEXPERIMENT=jsonv2       Streaming JSON decoder. Cuts allocs on the
#                             TwelveData WSS hot path when the live feed
#                             ships. Harmless for the offline subcommands.
#
# GOMAXPROCS is auto-detected in 1.25 inside containers; we don't override.
set -eu
export GOEXPERIMENT=greenteagc,jsonv2
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR/.."
exec ./go_trading_sweepbos_xauusd "$@"
