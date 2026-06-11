#!/usr/bin/env python3
"""
mt5bridge/execute.py -- place ONE GOLD-T order on the connected Admirals account,
sized to risk a fixed fraction of equity at the stop (Daniel's 38.5% plan).

The core is `place_order(...)`, a single effect-bearing entry point reused by
both this CLI (`__main__`) and the HTTP bridge (`server.py`). It assumes the
caller has already `mt5.initialize()`d (the CLI does it here; the long-lived
server does it once at startup) and never shuts MT5 down itself.

SAFETY GATES (all on by default):
 - DRY-RUN by default: runs mt5.order_check() which validates margin/volume/levels
   but PLACES NOTHING. Real placement requires the explicit arm flag.
 - ONE-AT-A-TIME: refuses if ANY open position OR pending order exists on the
   symbol (broker state is the source of truth, not the paper layer).
 - LIMIT with EXPIRATION: one pending LIMIT at the structural entry with SL/TP,
   `ORDER_TIME_SPECIFIED` expiring in N minutes (default 15 = 3x5m candles) so a
   stale level cannot fill hours later -- this aligns real execution with the
   backtest, which enters on the signal candle.
 - No averaging, no martingale, no modify.

Usage (run under wine python, terminal open + logged in):
  python execute.py --side SELL --entry 4319.80 --sl 4336.18 --tp 4288.55           # dry-run
  python execute.py --side SELL --entry 4319.80 --sl 4336.18 --tp 4288.55 --arm     # REAL order
"""

import argparse
import os
import sys
import time

from sizing import compute_lots

try:
    import MetaTrader5 as mt5
except ImportError:
    sys.exit("MetaTrader5 not importable -- run under wine python (see README).")

SYMBOL = os.environ.get("MT5_SYMBOL", "GOLD-T")
DEFAULT_RISK = float(os.environ.get("MT5_RISK_PCT", "0.385"))
# N candles of validity. 5m x 3 = 15min: a LIMIT that does not fill in the
# signal's window is cancelled by the broker (backtest enters on the bar).
EXPIRATION_MIN = int(os.environ.get("MT5_EXPIRATION_MIN", "15"))
# Stamps our orders/positions so the bridge can recognise its own fills and a
# human can spot them in the terminal. Arbitrary but stable.
MAGIC = int(os.environ.get("MT5_MAGIC", "770077"))
COMMENT = "sweepbos-auto"


def place_order(side, entry, sl, tp, risk_pct=DEFAULT_RISK, arm=False,
                *, symbol=SYMBOL, expiration_min=EXPIRATION_MIN, comment=COMMENT):
    """Validate (and, if arm, place) ONE pending LIMIT. Assumes mt5 is already
    initialized; does NOT shut it down. Trade-logic failures come back as
    {ok: False, reason: ...} -- this never raises for them.

    Returns a dict carrying everything the caller (CLI report, bridge log,
    WhatsApp notify, reconciliation) needs:
      ok, dry_run, reason, kind, login, server, side, symbol, entry, sl, tp,
      risk_pct, lots, contract_size, target_risk, actual_risk, detail,
      equity, margin, equity_after, free_after, retcode, comment, ticket,
      expiration
    """
    out = {
        "ok": False, "dry_run": not arm, "reason": "",
        "kind": "?", "login": None, "server": "",
        "side": side, "symbol": symbol, "entry": entry, "sl": sl, "tp": tp,
        "risk_pct": risk_pct, "lots": 0.0, "contract_size": 0.0,
        "target_risk": 0.0, "actual_risk": 0.0, "detail": "",
        "equity": 0.0, "margin": 0.0, "equity_after": 0.0, "free_after": 0.0,
        "retcode": None, "comment": "", "ticket": None, "expiration": None,
    }
    if side not in ("BUY", "SELL"):
        out["reason"] = f"bad side {side!r} (want BUY/SELL)"
        return out

    acc = mt5.account_info()
    si = mt5.symbol_info(symbol)
    if acc is None or si is None:
        out["reason"] = "no account/symbol -- is the terminal open AND logged in?"
        return out
    if not si.visible:
        mt5.symbol_select(symbol, True)
        si = mt5.symbol_info(symbol)

    out["kind"] = "REAL" if acc.trade_mode == getattr(mt5, "ACCOUNT_TRADE_MODE_REAL", 2) else "DEMO"
    out["login"], out["server"], out["equity"] = acc.login, acc.server, float(acc.equity)

    # ── one-at-a-time, checked against the BROKER ─────────────────────────
    npos = len(mt5.positions_get(symbol=symbol) or [])
    nord = len(mt5.orders_get(symbol=symbol) or [])
    if npos or nord:
        out["reason"] = (f"REFUSE one-at-a-time: {npos} open position(s) + "
                         f"{nord} pending order(s) on {symbol}")
        return out

    # ── sizing ────────────────────────────────────────────────────────────
    lots, actual, target, detail = compute_lots(
        acc.equity, risk_pct, entry, sl,
        si.trade_contract_size, si.volume_min, si.volume_step, si.volume_max)
    out.update(lots=float(lots), actual_risk=float(actual), target_risk=float(target),
               detail=detail, contract_size=float(si.trade_contract_size))
    if lots <= 0:
        out["reason"] = "computed 0 lots -- " + detail
        return out

    # ── build a pending LIMIT at the structural entry, with expiration ────
    otype = mt5.ORDER_TYPE_BUY_LIMIT if side == "BUY" else mt5.ORDER_TYPE_SELL_LIMIT
    tick = mt5.symbol_info_tick(symbol)
    # Base expiration on the BROKER's clock (tick.time, server tz) to avoid
    # host/server skew; fall back to host time only if no tick (market closed).
    base = int(tick.time) if (tick and getattr(tick, "time", 0)) else int(time.time())
    expiration = base + int(expiration_min) * 60
    req = {
        "action": mt5.TRADE_ACTION_PENDING,
        "symbol": symbol,
        "volume": float(lots),
        "type": otype,
        "price": float(entry),
        "sl": float(sl),
        "tp": float(tp),
        "type_time": mt5.ORDER_TIME_SPECIFIED,
        "expiration": expiration,
        "magic": MAGIC,
        "comment": comment,
    }
    out["expiration"] = expiration

    chk = mt5.order_check(req)
    if chk is None:
        out["reason"] = f"order_check returned None: {mt5.last_error()}"
        return out
    out["retcode"] = int(chk.retcode)
    out["comment"] = str(chk.comment)
    out["margin"] = float(getattr(chk, "margin", 0.0) or 0.0)
    out["equity_after"] = float(getattr(chk, "equity", 0.0) or 0.0)
    out["free_after"] = float(getattr(chk, "margin_free", 0.0) or 0.0)

    if not arm:
        # order_check uses retcode 0 for "all checks passed".
        out["ok"] = (chk.retcode == 0)
        out["reason"] = ("DRY-RUN -- validated, nothing placed" if out["ok"]
                         else f"order_check retcode={chk.retcode}: {chk.comment}")
        return out

    # ── arm guard: Algo-Trading must be ON and the symbol tradable ────────
    ti = mt5.terminal_info()
    if not (si.trade_mode != 0 and ti is not None and ti.trade_allowed):
        out["reason"] = "REFUSE arm: terminal Algo-Trading is OFF -- enable it in the MT5 toolbar first."
        return out

    res = mt5.order_send(req)
    if res is None:
        out["reason"] = f"order_send returned None: {mt5.last_error()}"
        return out
    out["retcode"] = int(res.retcode)
    out["comment"] = str(res.comment)
    out["ticket"] = int(getattr(res, "order", 0)) or None
    out["ok"] = (res.retcode == mt5.TRADE_RETCODE_DONE)
    out["reason"] = "PLACED" if out["ok"] else f"order_send retcode={res.retcode}: {res.comment}"
    return out


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--side", required=True, choices=["BUY", "SELL"])
    ap.add_argument("--entry", type=float, required=True)
    ap.add_argument("--sl", type=float, required=True)
    ap.add_argument("--tp", type=float, required=True)
    ap.add_argument("--risk", type=float, default=DEFAULT_RISK,
                    help="fraction of equity to risk at the SL (default 0.385)")
    ap.add_argument("--exp-min", type=int, default=EXPIRATION_MIN,
                    help="LIMIT validity in minutes (default 15 = 3x5m candles)")
    ap.add_argument("--arm", action="store_true",
                    help="actually place the order (default: dry-run order_check only)")
    args = ap.parse_args()

    if not mt5.initialize():
        sys.exit(f"initialize failed: {mt5.last_error()}")
    try:
        r = place_order(args.side, args.entry, args.sl, args.tp,
                        risk_pct=args.risk, arm=args.arm, expiration_min=args.exp_min)
    except Exception as e:
        mt5.shutdown()
        sys.exit(f"place_order raised: {e}")

    print(f"[{r['kind']}] {r['login']}@{r['server']}  equity=${r['equity']:.2f}")
    print(f"  {r['side']} {r['symbol']}  entry={r['entry']}  sl={r['sl']}  tp={r['tp']}")
    if r["detail"]:
        print(f"  sizing: {r['detail']}")
    if r["lots"] > 0 and r["actual_risk"] > r["target_risk"] * 1.25:
        print(f"  [!] actual risk ${r['actual_risk']:.2f} exceeds target ${r['target_risk']:.2f} "
              f"by >25% (min-lot/step). Re-check before --arm.")
    if r["retcode"] is not None:
        print(f"  order_check: retcode={r['retcode']} ({r['comment']!r})  "
              f"margin=${r['margin']:.2f}  equity_after=${r['equity_after']:.2f}  "
              f"free_after=${r['free_after']:.2f}")
    if r["ticket"]:
        print(f"  order_send: order_ticket={r['ticket']}")
    print(f"  => {'OK' if r['ok'] else 'FAIL'}: {r['reason']}")
    if not args.arm and r["ok"]:
        print("  DRY-RUN -- NO order placed. Add --arm to send for real.")

    mt5.shutdown()
    return 0 if r["ok"] else 1


if __name__ == "__main__":
    raise SystemExit(main())
