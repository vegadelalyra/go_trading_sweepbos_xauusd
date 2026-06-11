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
