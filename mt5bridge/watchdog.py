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
import time
import urllib.request

from bridgelib import format_watchdog_message

HEALTH_URL = os.environ.get("MT5_HEALTH_URL", "http://127.0.0.1:9099/health")
INTERVAL = int(os.environ.get("MT5_WATCH_INTERVAL", "60"))
WPP_ENDPOINT = os.environ.get("MT5_WPP_ENDPOINT", "http://127.0.0.1:8080/api/send")
WPP_PHONE = os.environ.get("MT5_WPP_PHONE", "573137054945")
WPP_NOTIFY = os.environ.get("MT5_WPP_NOTIFY", "1") == "1"


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
    print(f"[watchdog] polling {HEALTH_URL} every {INTERVAL}s (grace {grace}s)", flush=True)
    while True:
        healthy, detail = probe()
        if healthy != last:
            last = healthy
            warming = (time.time() - started) < grace
            if not warming:
                send_whatsapp(format_watchdog_message(healthy, detail))
            print(f"[watchdog] state change -> healthy={healthy} (warming={warming}): {detail}", flush=True)
        time.sleep(INTERVAL)


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        pass
