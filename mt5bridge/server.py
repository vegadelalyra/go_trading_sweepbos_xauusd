#!/usr/bin/env python3
"""
mt5bridge/server.py -- long-lived HTTP bridge between the Go detector and the
Admirals MT5 terminal (runs under wine python, same decoupled-service pattern as
go_wpp_bot). The Go side (paper-live -live-execute) fires-and-forgets a POST to
this server when it sees a green; the server places ONE pending LIMIT (sized to
risk% of equity at the SL) and, when that LIMIT actually FILLS, pushes a rich
WhatsApp message describing the opened position.

Binds 127.0.0.1:9099 (wine maps host loopback, so Go reaches it natively;
9091 is occupied on this box, override with MT5_BRIDGE_PORT).

Endpoints:
  POST /order  {side, entry, sl, tp, strategy, key}  -> place_order(); JSON result
  GET  /health                                       -> account/symbol/arm snapshot

SAFETY:
  - Arm mode is OFF unless env MT5_ARM=1. OFF => order_check only, nothing placed.
  - one-at-a-time vs the BROKER (enforced inside place_order).
  - each-setup-once: a `key` already acted on is rejected (defense-in-depth on
    top of the Go-side gate and the broker one-at-a-time). Seeded on startup
    from the last 24h of the order log so a restart does not re-fire.
  - HALT kill-switch: if ~/mt5wine-dl/HALT exists, every order is refused.
  - Startup reconciliation: existing positions are recorded so their (later)
    presence is never mistaken for a fresh fill.

Run:  MT5_ARM=0 ./run_wine.sh server      # dry-run bridge (default)
      MT5_ARM=1 ./run_wine.sh server      # ARMED -- real orders
"""

import json
import logging
import os
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer

from bridgelib import (signal_key, append_order, load_recent_keys,
                       format_fill_message, format_reject_message, format_expiry_message)
from execute import place_order, MAGIC, SYMBOL, DEFAULT_RISK, EXPIRATION_MIN

try:
    import MetaTrader5 as mt5
except ImportError:
    raise SystemExit("MetaTrader5 not importable -- run under wine python (see README).")

HOST = os.environ.get("MT5_BRIDGE_HOST", "127.0.0.1")
# 9091 is taken on this box by an IPv6-localhost node process; default to a
# free port. Override with MT5_BRIDGE_PORT; keep the Go -exec-endpoint in sync.
PORT = int(os.environ.get("MT5_BRIDGE_PORT", "9099"))
ARM = os.environ.get("MT5_ARM", "0") == "1"
RISK = float(os.environ.get("MT5_RISK_PCT", str(DEFAULT_RISK)))
POLL_SEC = int(os.environ.get("MT5_POLL_SEC", "3"))
DEDUP_WINDOW_SEC = int(os.environ.get("MT5_DEDUP_WINDOW_SEC", "86400"))  # 24h

DL = os.path.join(os.path.expanduser("~"), "mt5wine-dl")
ORDERS_LOG = os.environ.get("MT5_ORDERS_LOG", os.path.join(DL, "orders.log"))
HALT_FILE = os.environ.get("MT5_HALT_FILE", os.path.join(DL, "HALT"))

WPP_ENDPOINT = os.environ.get("MT5_WPP_ENDPOINT", "http://127.0.0.1:8080/api/send")
WPP_PHONE = os.environ.get("MT5_WPP_PHONE", "573137054945")
WPP_NOTIFY = os.environ.get("MT5_WPP_NOTIFY", "1") == "1"

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("mt5bridge")

# All MT5 access is serialized: the HTTP handler thread and the fill-poller
# thread share one IPC connection, which is not safe for concurrent calls.
MT5_LOCK = threading.Lock()

# each-setup-once: keys we have already acted on (real placement or validated
# dry-run). Seeded from the order log on startup so a restart does not re-fire.
placed_keys = set()
# placement records by order ticket, for best-effort strategy labelling on fill.
placed_orders = {}
# position tickets already known/notified, so reconciliation + poll never
# re-notify a pre-existing or already-announced position.
known_positions = set()
# pending LIMITs we placed for real, by order ticket, so the poll loop can alert
# when one EXPIRES without filling. Each value carries a HOST-clock deadline
# (placed_at + EXPIRATION_MIN), so host/broker timezone skew never affects expiry
# detection.
pending_tracked = {}


# ─────────────────────────────── helpers ──────────────────────────────────────

def log_order(record):
    """Append one JSON line to the configured order log."""
    append_order(ORDERS_LOG, record)


def send_whatsapp(text):
    """Fire a WhatsApp via go_wpp_bot. Never fatal -- log and move on (same
    discipline as the Go-side notify)."""
    if not WPP_NOTIFY:
        return
    try:
        body = json.dumps({"phone": WPP_PHONE, "message": text}).encode("utf-8")
        req = urllib.request.Request(WPP_ENDPOINT, data=body,
                                     headers={"Content-Type": "application/json"})
        urllib.request.urlopen(req, timeout=10).read()
        log.info("wpp sent (%d chars)", len(text))
    except Exception as e:
        log.warning("wpp notify failed: %s", e)


# ─────────────────────────────── MT5-backed ops ───────────────────────────────

def ensure_mt5():
    """Keep a live terminal connection. If the terminal was closed/restarted the
    IPC handle goes stale and account_info() returns None; re-initialize so the
    bridge re-attaches to the now-running terminal instead of silently refusing
    every order. Caller MUST hold MT5_LOCK. Returns True iff connected."""
    if mt5.account_info() is not None:
        return True
    log.warning("MT5 connection stale -- reinitializing (terminal restarted?)")
    mt5.shutdown()
    if not mt5.initialize():
        log.warning("MT5 reinitialize failed: %s", mt5.last_error())
        return False
    ok = mt5.account_info() is not None
    if ok:
        log.info("MT5 reconnected to terminal")
    return ok


def health_snapshot():
    with MT5_LOCK:
        ensure_mt5()
        acc = mt5.account_info()
        ti = mt5.terminal_info()
        npos = len(mt5.positions_get(symbol=SYMBOL) or [])
        nord = len(mt5.orders_get(symbol=SYMBOL) or [])
    if acc is None:
        return {"ok": False, "reason": "no account -- terminal logged in?"}
    kind = "REAL" if acc.trade_mode == getattr(mt5, "ACCOUNT_TRADE_MODE_REAL", 2) else "DEMO"
    return {
        "ok": True, "kind": kind, "login": acc.login, "server": acc.server,
        "equity": round(acc.equity, 2), "balance": round(acc.balance, 2),
        "free_margin": round(acc.margin_free, 2),
        "positions": npos, "orders": nord,
        "algo_trading": bool(ti.trade_allowed) if ti else False,
        "arm": ARM, "halt": os.path.exists(HALT_FILE),
        "symbol": SYMBOL, "risk_pct": RISK, "each_setup_once": len(placed_keys),
    }


def positions_snapshot():
    """Read-only detail of open positions on the symbol — distinguishes bot
    orders (our MAGIC) from anything opened by hand in the terminal."""
    with MT5_LOCK:
        ensure_mt5()
        positions = mt5.positions_get(symbol=SYMBOL) or []
        acc = mt5.account_info()
        out = []
        for p in positions:
            side = "BUY" if p.type == getattr(mt5, "POSITION_TYPE_BUY", 0) else "SELL"
            out.append({
                "ticket": p.ticket, "side": side, "volume": p.volume,
                "price_open": round(p.price_open, 2), "sl": round(p.sl, 2),
                "tp": round(p.tp, 2), "profit": round(p.profit, 2),
                "magic": getattr(p, "magic", 0), "comment": p.comment,
                "ours": getattr(p, "magic", 0) == MAGIC,
            })
    return {"count": len(out), "positions": out,
            "equity": round(acc.equity, 2) if acc else None,
            "balance": round(acc.balance, 2) if acc else None}


def reconcile_startup():
    """Seed known_positions (so pre-existing positions are never announced as
    fresh fills) and each-setup-once from the recent order log."""
    with MT5_LOCK:
        positions = mt5.positions_get(symbol=SYMBOL) or []
    for p in positions:
        known_positions.add(p.ticket)
    placed_keys.update(load_recent_keys(ORDERS_LOG, DEDUP_WINDOW_SEC, int(time.time())))
    log.info("reconcile: %d existing position(s) seeded, %d recent key(s) for each-setup-once",
             len(positions), len(placed_keys))


def detect_fills():
    """Under lock, return rich info dicts for positions that newly appeared
    (our MAGIC) since the last poll. WhatsApp/log happen by the caller, outside
    the lock (network)."""
    fresh = []
    with MT5_LOCK:
        if not ensure_mt5():
            return fresh
        positions = mt5.positions_get(symbol=SYMBOL) or []
        si = mt5.symbol_info(SYMBOL)
        acc = mt5.account_info()
        cs = float(si.trade_contract_size) if si else 100.0
        equity = float(acc.equity) if acc else 0.0
        free = float(acc.margin_free) if acc else 0.0
        kind = ("REAL" if acc and acc.trade_mode == getattr(mt5, "ACCOUNT_TRADE_MODE_REAL", 2)
                else "DEMO")
        for p in positions:
            if getattr(p, "magic", 0) != MAGIC or p.ticket in known_positions:
                continue
            known_positions.add(p.ticket)
            side = "BUY" if p.type == getattr(mt5, "POSITION_TYPE_BUY", 0) else "SELL"
            dist_sl = abs(p.price_open - p.sl) if p.sl else 0.0
            dist_tp = abs(p.tp - p.price_open) if p.tp else 0.0
            otype = mt5.ORDER_TYPE_BUY if side == "BUY" else mt5.ORDER_TYPE_SELL
            m = mt5.order_calc_margin(otype, SYMBOL, p.volume, p.price_open)
            rec = placed_orders.get(p.ticket) or _match_placed(p)
            fresh.append({
                "kind": kind, "side": side, "symbol": p.symbol, "ticket": p.ticket,
                "strategy": (rec or {}).get("strategy"),
                "price_open": p.price_open, "volume": p.volume, "sl": p.sl, "tp": p.tp,
                "loss_at_sl": p.volume * dist_sl * cs,
                "gain_at_tp": p.volume * dist_tp * cs,
                "margin": float(m) if m else 0.0, "equity": equity, "free": free,
            })
    return fresh


def _match_placed(p):
    """Best-effort: find the placement record whose SL/TP match this position
    (LIMIT fills at exact SL/TP), to recover the strategy label across the
    fill. Called under MT5_LOCK (touches only in-memory state)."""
    for rec in placed_orders.values():
        if round(rec.get("sl", 0), 2) == round(p.sl, 2) and round(rec.get("tp", 0), 2) == round(p.tp, 2):
            return rec
    return None


def detect_expiries(now):
    """Records for our pending LIMITs that are GONE from the order book, did NOT
    become a position, and are past their host-clock deadline -> expired unfilled.
    Pops them from pending_tracked so each expiry alerts at most once."""
    expired = []
    with MT5_LOCK:
        if not ensure_mt5():
            return expired
        live = {o.ticket for o in (mt5.orders_get(symbol=SYMBOL) or [])}
        positions = mt5.positions_get(symbol=SYMBOL) or []
        for ticket, rec in list(pending_tracked.items()):
            if ticket in live:
                continue  # still resting on the book
            filled = any(getattr(p, "magic", 0) == MAGIC
                         and round(p.sl, 2) == round(rec.get("sl", 0) or 0, 2)
                         and round(p.tp, 2) == round(rec.get("tp", 0) or 0, 2)
                         for p in positions)
            if filled:
                pending_tracked.pop(ticket, None)  # the fill path announces it
                continue
            if now >= rec.get("deadline", 0):
                pending_tracked.pop(ticket, None)
                expired.append({**rec, "ticket": ticket})
            # else: gone before deadline w/o a position (early cancel or a brief
            # fill-race) -> keep one cycle; resolves to filled or past-deadline.
    return expired


def poll_loop():
    while True:
        time.sleep(POLL_SEC)
        try:
            for info in detect_fills():
                log.info("FILL %s %s ticket=%s @%.2f vol=%g",
                         info["side"], info["symbol"], info["ticket"],
                         info["price_open"], info["volume"])
                log_order({"event": "fill", **{k: info[k] for k in
                          ("side", "symbol", "ticket", "strategy", "price_open",
                           "volume", "sl", "tp", "loss_at_sl", "gain_at_tp",
                           "margin", "equity")}})
                send_whatsapp(format_fill_message(info))
        except Exception as e:
            log.warning("poll error (fills): %s", e)
        try:
            for rec in detect_expiries(int(time.time())):
                log.info("EXPIRY %s ticket=%s entry=%.2f -- LIMIT expired unfilled",
                         rec.get("side"), rec.get("ticket"), float(rec.get("entry") or 0.0))
                log_order({"event": "expiry", **{k: rec.get(k) for k in
                          ("side", "symbol", "ticket", "strategy", "entry", "sl", "tp", "key")}})
                send_whatsapp(format_expiry_message(rec))
        except Exception as e:
            log.warning("poll error (expiry): %s", e)


# ─────────────────────────────── HTTP handler ─────────────────────────────────

class Handler(BaseHTTPRequestHandler):
    def log_message(self, *a):  # quiet the default per-request stderr spam
        pass

    def _json(self, code, obj):
        body = json.dumps(obj).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.split("?")[0] == "/health":
            self._json(200, health_snapshot())
        elif self.path.split("?")[0] == "/positions":
            self._json(200, positions_snapshot())
        else:
            self._json(404, {"ok": False, "reason": "not found"})

    def do_POST(self):
        if self.path.split("?")[0] != "/order":
            return self._json(404, {"ok": False, "reason": "not found"})
        try:
            n = int(self.headers.get("Content-Length", 0))
            body = json.loads(self.rfile.read(n) or b"{}")
        except (ValueError, TypeError) as e:
            return self._json(400, {"ok": False, "reason": f"bad json: {e}"})

        side = body.get("side")
        try:
            entry = float(body["entry"]); sl = float(body["sl"]); tp = float(body["tp"])
        except (KeyError, TypeError, ValueError):
            return self._json(400, {"ok": False, "reason": "need numeric entry, sl, tp"})
        strategy = body.get("strategy", "")
        key = body.get("key") or signal_key(strategy, side, entry, sl, tp)
        # The caller may request a risk fraction, but MT5_RISK_PCT is a HARD
        # ceiling on the money side: the bridge never risks more than configured
        # here, whatever Go sends.
        try:
            req_risk = float(body.get("risk_pct", RISK))
        except (TypeError, ValueError):
            req_risk = RISK
        risk = min(max(req_risk, 0.0), RISK)

        # ── kill-switch ──────────────────────────────────────────────────
        if os.path.exists(HALT_FILE):
            log.warning("HALT present -- refusing order key=%s", key)
            return self._json(200, {"ok": False, "halted": True,
                                    "reason": f"HALT file present at {HALT_FILE}"})

        # ── each-setup-once ──────────────────────────────────────────────
        if key in placed_keys:
            return self._json(200, {"ok": False, "dedup": True,
                                    "reason": "each-setup-once: key already acted on", "key": key})

        with MT5_LOCK:
            if not ensure_mt5():
                return self._json(200, {"ok": False, "key": key,
                                        "reason": "MT5 not connected -- is the terminal open AND logged in?"})
            r = place_order(side, entry, sl, tp, risk_pct=risk, arm=ARM)

        r["key"] = key
        r["strategy"] = strategy
        if r.get("ok"):
            placed_keys.add(key)
            if r.get("ticket"):
                placed_orders[r["ticket"]] = {"strategy": strategy, "key": key,
                                              "side": side, "entry": entry, "sl": sl, "tp": tp}
                # Track REAL pending LIMITs (dry-runs have no ticket) so the poll
                # loop can alert if this one EXPIRES unfilled. Deadline on the HOST
                # clock so host/broker tz skew never affects expiry detection.
                if not r["dry_run"]:
                    pending_tracked[r["ticket"]] = {
                        "strategy": strategy, "key": key, "side": side,
                        "symbol": SYMBOL, "entry": entry, "sl": sl, "tp": tp,
                        "deadline": int(time.time()) + EXPIRATION_MIN * 60,
                    }
            log_order({"event": "placed" if not r["dry_run"] else "dryrun",
                       "key": key, "strategy": strategy, "side": side,
                       "entry": entry, "sl": sl, "tp": tp, "lots": r["lots"],
                       "actual_risk": r["actual_risk"], "ticket": r.get("ticket"),
                       "retcode": r.get("retcode"), "reason": r["reason"]})
        elif not r["dry_run"] and r.get("sent"):
            # A REAL order reached the broker and was definitively rejected
            # (transient retries already exhausted in place_order). Alert so the
            # failure is never silent. one-at-a-time / 0-lot skips have sent=False
            # and stay quiet (expected, not a broker rejection).
            log_order({"event": "reject", "key": key, "strategy": strategy,
                       "side": side, "entry": entry, "sl": sl, "tp": tp,
                       "retcode": r.get("retcode"), "attempts": r.get("attempts"),
                       "reason": r["reason"]})
            send_whatsapp(format_reject_message(r))
        log.info("ORDER key=%s side=%s -> ok=%s dry=%s reason=%s lots=%s ticket=%s",
                 key, side, r.get("ok"), r.get("dry_run"), r.get("reason"),
                 r.get("lots"), r.get("ticket"))
        return self._json(200, r)


def main():
    if not mt5.initialize():
        raise SystemExit(f"mt5.initialize failed: {mt5.last_error()} -- terminal open AND logged in?")
    snap = health_snapshot()
    mode = "ARMED (REAL ORDERS)" if ARM else "DRY-RUN (order_check only)"
    log.info("MT5 bridge up: %s  symbol=%s risk=%.3f  poll=%ds", mode, SYMBOL, RISK, POLL_SEC)
    log.info("account: %s", snap)
    if snap.get("kind") == "REAL" and ARM:
        log.warning("!!! ARMED on a REAL account (%s@%s) -- orders will use real money !!!",
                    snap.get("login"), snap.get("server"))
    reconcile_startup()

    threading.Thread(target=poll_loop, daemon=True).start()

    httpd = HTTPServer((HOST, PORT), Handler)
    log.info("listening on http://%s:%d  (POST /order, GET /health)", HOST, PORT)
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        httpd.server_close()
        mt5.shutdown()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
