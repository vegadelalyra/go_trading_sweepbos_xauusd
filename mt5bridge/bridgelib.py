"""Pure / MT5-free helpers for the bridge, so they unit-test on plain Linux
python3 (server.py itself imports MetaTrader5 and only runs under wine)."""

import json
import os
import time


def signal_key(strategy, side, entry, sl, tp):
    """Fallback dedup identity when the caller sends no explicit key."""
    return f"{strategy}|{side}|{entry}|{sl}|{tp}"


def append_order(path, record):
    """Append one JSON line to the order log (audit + cross-restart
    each-setup-once seeding). Stamps `ts` if absent. Never raises."""
    record = dict(record)
    record.setdefault("ts", int(time.time()))
    try:
        if os.path.dirname(path):
            os.makedirs(os.path.dirname(path), exist_ok=True)
        with open(path, "a", encoding="utf-8") as f:
            f.write(json.dumps(record) + "\n")
    except Exception:
        pass
    return record


def load_recent_keys(path, within_sec, now):
    """Keys of real placements (event == 'placed') within the last `within_sec`
    seconds, for seeding each-setup-once across a restart. Pure over file bytes."""
    keys = set()
    try:
        with open(path, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    r = json.loads(line)
                except ValueError:
                    continue
                if r.get("event") != "placed":
                    continue
                if now - int(r.get("ts", 0)) > within_sec:
                    continue
                if r.get("key"):
                    keys.add(r["key"])
    except FileNotFoundError:
        pass
    except Exception:
        pass
    return keys


def format_fill_message(d):
    """Rich, ASCII-only WhatsApp text for a freshly OPENED position. Pure.

    Expects keys: kind, side, symbol, strategy, price_open, volume, sl, tp,
    loss_at_sl, gain_at_tp, margin, equity, free, ticket."""
    lines = [
        f"OPERACION ABIERTA ({d['kind']})",
        f"{d['side']} {d['symbol']}  {d.get('strategy') or '?'}",
        f"entry(fill)={d['price_open']:.2f}  lots={d['volume']:g}",
        (f"SL={d['sl']:.2f}  TP={d['tp']:.2f}" if d.get("tp") else f"SL={d['sl']:.2f}  TP=none"),
    ]
    if d.get("loss_at_sl"):
        pct = (d["loss_at_sl"] / d["equity"] * 100.0) if d.get("equity") else 0.0
        lines.append(f"perdida@SL=${d['loss_at_sl']:.2f} ({pct:.1f}% equity)")
    if d.get("gain_at_tp"):
        rr = (d["gain_at_tp"] / d["loss_at_sl"]) if d.get("loss_at_sl") else 0.0
        lines.append(f"ganancia@TP=${d['gain_at_tp']:.2f}  RR={rr:.2f}")
    lines.append(f"margen=${d.get('margin', 0.0):.2f}  equity=${d.get('equity', 0.0):.2f}  "
                 f"libre=${d.get('free', 0.0):.2f}")
    lines.append(f"ticket={d.get('ticket')}")
    return "\n".join(lines)


# ───────────────────────── retcode classification + retry ─────────────────────
#
# MetaTrader5 trade-server return codes (subset), names per the MQL5 docs. Kept
# here (MT5-free) so the retry policy and classifier unit-test on plain python3.
RETCODE_NAMES = {
    0: "OK", 10009: "DONE",
    10004: "REQUOTE", 10006: "REJECT", 10013: "INVALID", 10014: "INVALID_VOLUME",
    10015: "INVALID_PRICE", 10016: "INVALID_STOPS", 10017: "TRADE_DISABLED",
    10018: "MARKET_CLOSED", 10019: "NO_MONEY", 10021: "PRICE_OFF",
    10024: "TOO_MANY_REQUESTS", 10027: "CLIENT_DISABLES_AT", 10031: "CONNECTION",
}

# Retry ONLY on these transient, server-side conditions — the order was NOT
# placed (no connection / requote / no prices), so a bounded re-send inside the
# signal window is safe and recovers the failure modes seen in production
# (notably 10031). Anything NOT in this set is treated as terminal: fail fast,
# alert, never retry (e.g. 10015 invalid price, 10016 invalid stops, 10018 market
# closed, 10019 no money, 10027 algo disabled).
TRANSIENT_RETCODES = frozenset({10004, 10021, 10031})


def is_transient_retcode(rc):
    """True iff `rc` is a transient trade-server condition worth retrying.
    Non-numeric / unknown / None -> False (treated as terminal)."""
    try:
        return int(rc) in TRANSIENT_RETCODES
    except (TypeError, ValueError):
        return False


def retcode_name(rc):
    """Human name for a retcode, falling back to its number as a string."""
    try:
        return RETCODE_NAMES.get(int(rc), str(rc))
    except (TypeError, ValueError):
        return str(rc)


def send_with_retry(order_send, classify_transient, done_retcode, *,
                    attempts=3, backoff=4.0, max_sec=20.0,
                    sleep=time.sleep, clock=time.time):
    """Call `order_send()` up to `attempts` times, retrying ONLY while the result
    is transient. Pure w.r.t. the injected `sleep`/`clock`, so it unit-tests with
    no MetaTrader5 and no real wall-clock.

      order_send():            callable -> broker result (or None on IPC error).
      classify_transient(res): True if this result should be retried.
      done_retcode:            retcode meaning success (TRADE_RETCODE_DONE).

    Stops on: success, a non-transient result, attempts exhausted, or when one
    more backoff would exceed `max_sec` (keeps retries well inside the LIMIT's
    15-min window). Returns (last_result, tries, retries) where `retries` lists
    {try, retcode} for each retried failure (for logging/alerts)."""
    started = clock()
    tries = 0
    retries = []
    while True:
        tries += 1
        res = order_send()
        rc = None if res is None else getattr(res, "retcode", None)
        if res is not None and rc == done_retcode:
            return res, tries, retries
        if (not classify_transient(res)) or tries >= attempts or \
           (clock() - started + backoff > max_sec):
            return res, tries, retries
        retries.append({"try": tries, "retcode": rc})
        sleep(backoff)


def _f(v):
    """ASCII-safe 2-decimal float for messages; passes non-numerics through."""
    try:
        return f"{float(v):.2f}"
    except (TypeError, ValueError):
        return str(v)


def format_reject_message(d):
    """ASCII-only WhatsApp text for a REAL order definitively rejected by the
    broker (transient retries already exhausted). Keys: kind, side, symbol,
    strategy, entry, sl, tp, retcode, attempts, reason."""
    return "\n".join([
        f"ORDEN RECHAZADA ({d.get('kind', '?')})",
        f"{d.get('side')} {d.get('symbol', 'GOLD-T')}  {d.get('strategy') or '?'}",
        f"entry={_f(d.get('entry'))}  SL={_f(d.get('sl'))}  TP={_f(d.get('tp'))}",
        f"retcode={d.get('retcode')} ({retcode_name(d.get('retcode'))})  intentos={d.get('attempts', '?')}",
        f"motivo: {d.get('reason', '')}",
    ])


def format_expiry_message(d):
    """ASCII-only WhatsApp text for a pending LIMIT that expired WITHOUT filling.
    Keys: side, symbol, strategy, entry, sl, tp, ticket."""
    return "\n".join([
        "LIMIT EXPIRADA SIN LLENAR",
        f"{d.get('side')} {d.get('symbol', 'GOLD-T')}  {d.get('strategy') or '?'}",
        f"entry={_f(d.get('entry'))}  SL={_f(d.get('sl'))}  TP={_f(d.get('tp'))}",
        f"ticket={d.get('ticket')}",
        "la senal no se ejecuto en su ventana de 15min",
    ])


def format_watchdog_message(healthy, detail):
    """ASCII-only WhatsApp text for a bridge health state change."""
    head = "WATCHDOG: ejecucion RECUPERADA" if healthy else "WATCHDOG: ejecucion CAIDA"
    return f"{head}\n{detail}"


def daily_loss_breached(equity, day_start_equity, max_loss_pct):
    """True if `equity` has fallen more than `max_loss_pct` (a fraction, e.g. 0.5
    for 50%) below `day_start_equity`. A hard circuit breaker against the
    38.5%-flat drawdown. max_loss_pct<=0 (or a missing/invalid baseline) disables
    the check -> returns False (no behavior change). Pure -> unit-testable."""
    try:
        if not max_loss_pct or max_loss_pct <= 0:
            return False
        if not day_start_equity or day_start_equity <= 0:
            return False
        return float(equity) <= float(day_start_equity) * (1.0 - float(max_loss_pct))
    except (TypeError, ValueError):
        return False
