#!/usr/bin/env python3
"""
mt5bridge/connect.py — READ-ONLY MetaTrader 5 connection health-check for the
Admirals account that will (in a later, deliberate session) execute the
sweepbos XAUUSD strategy.

THIS SCRIPT PLACES NO ORDERS. It attaches to a running MT5 terminal, reads
account / symbol / position state, and prints a readiness report. There is no
`order_send` anywhere in this file — by design. Order execution is a separate
next step once the real-account trading strategy is defined.

Safest mode (default): attach to an MT5 terminal you have ALREADY logged into
by hand in the GUI. No credentials touch this script or any file.

Optional headless mode: if MT5_LOGIN / MT5_PASSWORD / MT5_SERVER are set in the
environment, log in programmatically. Use only on a dedicated VPS.

Run WHERE MT5 LIVES (Windows native, or Linux under Wine — see README.md):
    python connect.py
"""

import os
import sys

try:
    import MetaTrader5 as mt5
except ImportError:
    sys.exit(
        "MetaTrader5 package not importable.\n"
        "  Windows/VPS:  pip install MetaTrader5\n"
        "  Linux:        the package is Windows-native — run under Wine (see README.md).\n"
    )

# Admirals SC names spot gold "GOLD-T" (not "XAUUSD"); override via MT5_SYMBOL.
SYMBOL = os.environ.get("MT5_SYMBOL", "GOLD-T")


def fail(msg):
    err = mt5.last_error()
    mt5.shutdown()
    sys.exit(f"FAIL: {msg}\n  mt5.last_error() = {err}")


def main():
    # ── 1. Connect ────────────────────────────────────────────────────────
    path = os.environ.get("MT5_PATH")  # explicit terminal64.exe path (optional)
    init_kwargs = {"path": path} if path else {}
    login = os.environ.get("MT5_LOGIN")

    if login:
        # Headless login (VPS). Credentials come from the environment only —
        # never hardcoded, never logged.
        ok = mt5.initialize(
            login=int(login),
            password=os.environ.get("MT5_PASSWORD", ""),
            server=os.environ.get("MT5_SERVER", ""),
            **init_kwargs,
        )
        mode = f"headless login {login}@{os.environ.get('MT5_SERVER', '?')}"
    else:
        # Attach to the already-logged-in GUI terminal — no credentials needed.
        ok = mt5.initialize(**init_kwargs)
        mode = "attach to running terminal (no credentials)"

    if not ok:
        fail(f"mt5.initialize() failed ({mode})")
    print(f"[OK] connected - {mode}")
    print(f"  MetaTrader5 pkg: {mt5.__version__}")

    ti = mt5.terminal_info()
    if ti is not None:
        print(f"  terminal: build={ti.build}  connected={ti.connected}  trade_allowed={ti.trade_allowed}")
        if not ti.trade_allowed:
            print("  [!] terminal trade_allowed=False - enable 'Algo Trading' in the toolbar "
                  "(only matters for the later execution step)")

    # ── 2. Account — surface REAL vs DEMO loudly ──────────────────────────
    acc = mt5.account_info()
    if acc is None:
        fail("account_info() returned None — is the terminal logged in?")
    kind = {
        getattr(mt5, "ACCOUNT_TRADE_MODE_DEMO", 0): "DEMO",
        getattr(mt5, "ACCOUNT_TRADE_MODE_REAL", 2): "REAL",
    }.get(acc.trade_mode, f"trade_mode={acc.trade_mode}")
    print("\n-- Account --")
    print(f"  login={acc.login}  server={acc.server}  [{kind}]")
    print(f"  company={acc.company}")
    print(f"  balance={acc.balance:.2f} {acc.currency}  equity={acc.equity:.2f}  free_margin={acc.margin_free:.2f}")
    print(f"  leverage=1:{acc.leverage}  account.trade_allowed={acc.trade_allowed}")

    # ── 3. Symbol — XAUUSD naming varies per broker ───────────────────────
    print(f"\n-- Symbol {SYMBOL} --")
    si = mt5.symbol_info(SYMBOL)
    if si is None:
        cands = [s.name for s in (mt5.symbols_get() or [])
                 if "XAU" in s.name.upper() or "GOLD" in s.name.upper()]
        print(f"  [X] '{SYMBOL}' not found. Gold-like symbols on this account: {cands or 'none'}")
        print("    set MT5_SYMBOL to the correct one and re-run.")
    else:
        if not si.visible:
            mt5.symbol_select(SYMBOL, True)
            si = mt5.symbol_info(SYMBOL)
        tick = mt5.symbol_info_tick(SYMBOL)
        print(f"  visible={si.visible}  digits={si.digits}  trade_mode={si.trade_mode}")
        print(f"  volume_min={si.volume_min}  volume_step={si.volume_step}  volume_max={si.volume_max}")
        print(f"  contract_size={si.trade_contract_size}")
        if tick is not None:
            print(f"  bid={tick.bid}  ask={tick.ask}  spread={tick.ask - tick.bid:.3f}")

    # ── 4. Open positions (read-only) ─────────────────────────────────────
    pos = mt5.positions_get() or []
    print(f"\n-- Open positions: {len(pos)} --")
    for p in pos:
        side = "BUY" if p.type == getattr(mt5, "POSITION_TYPE_BUY", 0) else "SELL"
        print(f"  #{p.ticket}  {p.symbol}  {side}  vol={p.volume}  entry={p.price_open}  "
              f"sl={p.sl}  tp={p.tp}  pnl={p.profit:.2f}")

    print("\n[OK] READ-ONLY health-check complete - NO orders were placed.")
    mt5.shutdown()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
