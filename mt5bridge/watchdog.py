#!/usr/bin/env python3
"""Standalone health watchdog for the mt5bridge.

Polls GET /health every MT5_WATCH_INTERVAL seconds and sends ONE WhatsApp on each
state CHANGE (healthy <-> down). 'Down' means: the bridge is unreachable, health
reports ok:false (MT5 disconnected / not logged in), or algo_trading:false.

It runs as its OWN process/service (NSSM: sweepbos-watchdog) on purpose -- a thread
inside the bridge could not alert when the bridge itself is the thing that died.
It is the safety net: you find out within ~60s that execution is down.

Env:
  MT5_HEALTH_URL    (default http://127.0.0.1:9099/health)
  MT5_WATCH_INTERVAL seconds between probes (default 60)
  MT5_WPP_ENDPOINT  go_wpp_bot send endpoint (default http://127.0.0.1:8080/api/send)
  MT5_WPP_PHONE     destination number (default 573137054945)
  MT5_WPP_NOTIFY    "1" to actually send (default 1)
"""

import json
import os
import re
import subprocess
import time
import urllib.request

from bridgelib import format_watchdog_message

HEALTH_URL = os.environ.get("MT5_HEALTH_URL", "http://127.0.0.1:9099/health")
INTERVAL = int(os.environ.get("MT5_WATCH_INTERVAL", "60"))
WPP_ENDPOINT = os.environ.get("MT5_WPP_ENDPOINT", "http://127.0.0.1:8080/api/send")
WPP_PHONE = os.environ.get("MT5_WPP_PHONE", "573137054945")
WPP_NOTIFY = os.environ.get("MT5_WPP_NOTIFY", "1") == "1"

# --- wpp-bot self-heal -------------------------------------------------------
# The wpp-bot can keep its HTTP API up while its underlying WhatsApp websocket is
# dead (e.g. DNS not ready at boot -> initial dial fails -> never reconnects).
# In that state EVERY /api/send returns 500 and alerts vanish SILENTLY. This
# watchdog (LocalSystem) tails the bot's error log; when it sees a send failure
# newer than the last one it healed, it restarts the service and tells Daniel an
# alert was dropped -- so a dead notifier can never stay silent for more than one
# poll interval. Wrapped in its own try/except: it can NEVER break bridge alerts.
WPP_ERR_LOG = os.environ.get("MT5_WPP_ERR_LOG", r"C:\sweepbos\logs\wppbot.err.log")
WPP_SERVICE = os.environ.get("MT5_WPP_SERVICE", "sweepbos-wppbot")
WPP_HEAL_STATE = os.environ.get("MT5_WPP_HEAL_STATE", r"C:\sweepbos\logs\wpp-heal.state")
_TS_RE = re.compile(r"^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2})")


def latest_wpp_failure():
    """Newest 'send failed' timestamp (str, sortable) in the bot err log, or ''."""
    try:
        with open(WPP_ERR_LOG, "r", encoding="utf-8", errors="ignore") as fh:
            lines = fh.readlines()
    except FileNotFoundError:
        return ""
    for line in reversed(lines):
        if "[API/send]" in line and "failed" in line:
            m = _TS_RE.match(line)
            if m:
                return m.group(1)
    return ""


def _read_heal_state():
    try:
        with open(WPP_HEAL_STATE, "r", encoding="utf-8") as fh:
            return fh.read().strip()
    except FileNotFoundError:
        return ""


def _write_heal_state(ts):
    try:
        with open(WPP_HEAL_STATE, "w", encoding="utf-8") as fh:
            fh.write(ts)
    except OSError as e:
        print("[watchdog] heal-state write failed:", e, flush=True)


def _wpp_send_retry(text, tries=8, gap=5):
    """Send via the bot, retrying while its socket finishes reconnecting."""
    body = json.dumps({"phone": WPP_PHONE, "message": text}).encode("utf-8")
    for i in range(tries):
        try:
            req = urllib.request.Request(
                WPP_ENDPOINT, data=body, headers={"Content-Type": "application/json"})
            urllib.request.urlopen(req, timeout=10).read()
            return True
        except Exception:
            time.sleep(gap)
    return False


def heal_wpp_if_needed():
    """Restart the wpp-bot if a send failed since we last healed; then notify."""
    ts = latest_wpp_failure()
    if not ts or ts <= _read_heal_state():
        return
    print(f"[watchdog] wpp send failure at {ts} -> restarting {WPP_SERVICE}", flush=True)
    subprocess.run(
        ["powershell", "-NoProfile", "-Command", f"Restart-Service {WPP_SERVICE} -Force"],
        capture_output=True, timeout=60)
    time.sleep(10)  # let whatsmeow re-dial the websocket
    note = ("⚠️ sweepbos: el notificador WhatsApp se cayo (ultimo fallo "
            f"{ts}) y un aviso pudo perderse. Reinicie el wpp-bot y reconecte. "
            "Revisa el detector/orders.log por si hubo una senal en ese rato.")
    if _wpp_send_retry(note):
        print("[watchdog] wpp healed + recovery notice sent", flush=True)
    else:
        print("[watchdog] wpp restarted but recovery send still failing", flush=True)
    _write_heal_state(ts)  # mark handled either way; next NEW failure re-triggers


def send_whatsapp(text):
    if not WPP_NOTIFY:
        print("[watchdog] (notify off):", text.replace("\n", " | "))
        return
    try:
        body = json.dumps({"phone": WPP_PHONE, "message": text}).encode("utf-8")
        req = urllib.request.Request(WPP_ENDPOINT, data=body,
                                     headers={"Content-Type": "application/json"})
        urllib.request.urlopen(req, timeout=10).read()
        print("[watchdog] wpp sent:", text.replace("\n", " | "))
    except Exception as e:
        print("[watchdog] wpp failed:", e)


def probe():
    """Return (healthy: bool, detail: str)."""
    try:
        with urllib.request.urlopen(HEALTH_URL, timeout=8) as resp:
            h = json.loads(resp.read())
    except Exception as e:
        return False, f"bridge unreachable: {e}"
    if not h.get("ok"):
        return False, f"MT5 not connected: {h.get('reason', '?')}"
    if not h.get("algo_trading"):
        return False, "Algo-Trading OFF in terminal"
    return True, f"OK equity=${h.get('equity')} {h.get('login')}@{h.get('server')}"


def main():
    # Assume healthy at start; only alert on CHANGES. A startup grace window
    # suppresses alerts while MT5/bridge are still coming up after a reboot, so we
    # don't fire a spurious CAIDA->RECUPERADA pair on every boot. State is still
    # tracked during grace; real changes after it alert normally.
    last = True
    grace = int(os.environ.get("MT5_WATCH_GRACE", "120"))
    started = time.time()
    # Seed the heal marker with any pre-existing failure so we don't restart the
    # wpp-bot for history on first boot -- only NEW failures trigger a heal.
    if not _read_heal_state():
        _write_heal_state(latest_wpp_failure())
    print(f"[watchdog] polling {HEALTH_URL} every {INTERVAL}s (grace {grace}s)", flush=True)
    while True:
        healthy, detail = probe()
        if healthy != last:
            last = healthy
            warming = (time.time() - started) < grace
            if not warming:
                send_whatsapp(format_watchdog_message(healthy, detail))
            print(f"[watchdog] state change -> healthy={healthy} (warming={warming}): {detail}", flush=True)
        # Self-heal the notifier. Isolated so a bug here can never kill the loop
        # that watches actual trade execution.
        try:
            heal_wpp_if_needed()
        except Exception as e:
            print("[watchdog] wpp heal check error:", e, flush=True)
        time.sleep(INTERVAL)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        pass
