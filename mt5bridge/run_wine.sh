#!/usr/bin/env bash
# mt5bridge/run_wine.sh — local Wine MT5 runtime helper (READ-ONLY connection).
#
# The Wine prefix, Python 3.11, the MetaTrader5 package (with numpy 1.26.4, the
# wine-compatible pin) and the MT5 terminal are already installed under
# ~/mt5wine. This wrapper just sets the Wine env and runs the right thing.
#
#   ./run_wine.sh terminal   # launch MT5 GUI — log into your REAL Admirals account here
#   ./run_wine.sh connect    # READ-ONLY health-check via wine python (NO orders)
#   ./run_wine.sh install    # (re)run the MT5 installer, only if ever needed
#
# connect.py contains no order_send — this whole layer only reads. Order
# execution is a separate, deliberate session.
set -uo pipefail
export WINEPREFIX="$HOME/mt5wine"
export WINEARCH=win64
export WINEDEBUG=-all
export WINEDLLOVERRIDES="mscoree=d;mshtml=d"
# Force UTF-8 stdout: wine's Python defaults to cp1252 and crashes on any
# non-latin glyph in output.
export PYTHONUTF8=1
export PYTHONIOENCODING=utf-8
# Admirals SC spot-gold symbol is GOLD-T (NOT XAUUSD). Override per run if needed.
export MT5_SYMBOL="${MT5_SYMBOL:-GOLD-T}"

PY="$WINEPREFIX/drive_c/Python311/python.exe"
DL="$HOME/mt5wine-dl"
HERE="$(cd "$(dirname "$0")" && pwd)"
TERM_EXE="$(find "$WINEPREFIX/drive_c" -iname terminal64.exe 2>/dev/null | head -1)"
# Note: we deliberately do NOT export MT5_PATH. With it set, connect.py's
# initialize(path=...) launches a fresh (un-logged-in) terminal and blocks on an
# IPC timeout. Instead `connect` attaches to the terminal you already opened and
# logged in via `terminal` below.

case "${1:-}" in
  terminal)
    [ -n "$TERM_EXE" ] || { echo "terminal64.exe not found — run: $0 install"; exit 1; }
    echo "Launching MT5. Log into your REAL Admirals account (File → Login to Trade Account):"
    echo "  login=<your number>  password=<yours>  server=<e.g. Admirals-Live>"
    exec wine "$TERM_EXE" ;;
  connect)
    echo "READ-ONLY health-check (no orders). MT5 must be open AND logged in."
    exec wine "$PY" "$HERE/connect.py" ;;
  execute)
    shift
    # Dry-run by default (order_check, no placement); pass --arm to send real.
    exec wine "$PY" "$HERE/execute.py" "$@" ;;
  server)
    # Long-lived HTTP bridge on 127.0.0.1:9091. DRY-RUN unless MT5_ARM=1 in env.
    echo "MT5 bridge (MT5_ARM=${MT5_ARM:-0}). MT5 must be open AND logged in."
    exec wine "$PY" "$HERE/server.py" ;;
  install)
    exec wine "$DL/mt5setup.exe" /auto ;;
  *)
    echo "usage: $0 {terminal|connect|execute [--arm ...]|server|install}"; exit 2 ;;
esac
