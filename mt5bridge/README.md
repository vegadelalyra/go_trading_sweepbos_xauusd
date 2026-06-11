# mt5bridge — MetaTrader 5 connection layer for Admirals

Read-only connection to the **real Admirals account** that will later execute
the sweepbos XAUUSD strategy. This first step **only proves the pipe**: it
attaches to a running MT5 terminal and reads account / symbol / position state.

**It places NO orders.** `connect.py` contains no `order_send`. Order execution
is a separate, deliberate next step (a future session, once the real-account
trading strategy is defined). Treat anything in this directory as the transport,
never the trader.

## Architecture (decoupled service, like `go_wpp_bot`)

The Go system stays the brain: it detects signals (SMC composite + dynamic
threshold + weights from Postgres). The MT5 Python API is Windows-native, so
execution can't live inside the Go binary — it runs as a separate process
**where MT5 runs**, and (later) the Go side will talk to it over a small local
HTTP/socket boundary, exactly like the WhatsApp bot. For now there is just the
`connect.py` health-check.

## Why Python + MT5 (not REST/FIX)

Admirals is a MetaTrader 4/5 broker with **no public retail REST API**.
Automation goes through MT5: either an in-terminal Expert Advisor (MQL5) or the
official `MetaTrader5` Python package (`mt5.order_send`). We chose the Python
API so the validated Go strategy stays the single source of truth instead of
being re-implemented in MQL5. (FIX is Admirals' institutional/pro path — Phase
4.3, for later / higher AUM.)

## Local Wine runtime — INSTALLED (2026-06-08)

A working Wine runtime is already set up on this Debian box (read-only test
home; the VPS remains the eventual live home). What's installed under
`~/mt5wine`:

- Wine 8.0 (Debian), prefix `~/mt5wine` in **win10** mode.
- Python 3.11.9 at `C:\Python311`.
- `MetaTrader5` 5.0.5735 **+ numpy pinned to 1.26.4** — numpy 2.x calls
  `ucrtbase.dll.crealf`, which Wine 8.0 doesn't implement, so it crashes on
  import; 1.26.4 is the wine-compatible pin. Keep numpy < 2 here.
- MT5 terminal at `C:\Program Files\MetaTrader 5\terminal64.exe`.

Suspend was also disabled system-wide (lid/idle) so the services (and this
runtime) survive a closed lid — see the repo's logind drop-in + masked sleep
targets.

**Two steps remain, and they are yours (credentials):**

```bash
cd mt5bridge
./run_wine.sh terminal    # MT5 opens — log into your REAL Admirals account, leave it open
./run_wine.sh connect     # READ-ONLY: prints REAL/DEMO, balance, XAUUSD specs, positions
```

Paste the `connect` output back. Verified pre-login: `connect` runs under wine
python and reports cleanly (`IPC timeout`) when no terminal is logged in — no
crash. ⚠️ Wine local is a **throwaway test runtime**; a closed-lid laptop still
risks thermals/uptime — the Windows VPS is the home for anything real.

## Runtime — pick one

The `MetaTrader5` package is **Windows-only**; on Linux it must run under Wine.

- **Windows VPS (recommended for anything heading to live).** MT5 runs natively,
  no Wine fiddling, and it stays up 24/5 — unlike this laptop, whose suspend
  pauses the services (a known issue). ~$10–15/mo. Best reliability.
- **Local Linux + Wine (free, fine for a first read-only test).** Install Wine,
  install the Windows MT5 build under it, then run a Windows Python (inside the
  same Wine prefix) with `MetaTrader5`. Bridges like `mt5linux` / `mt5-remote`
  (RPyC) help drive it from native Linux. Fragile; good enough to prove the
  connection, not for unattended live trading.

## Windows VPS quickstart (chosen runtime)

1. **Provision** a Windows VPS: Windows Server 2019/2022, 2 vCPU / 4 GB RAM /
   ~40 GB disk is plenty (MT5 is light). Pick an **EU region** (Admirals is
   EU-based). A generic cloud Windows VPS (Vultr/Contabo/AWS) is fine — a 5m
   LIMIT-order strategy is not latency-sensitive yet, so a forex-colocated VPS
   isn't needed at this stage.
2. **RDP in** from your Debian box (example):
   `xfreerdp /u:Administrator /v:<vps-ip> /dynamic-resolution` (install
   `freerdp2-x11` first).
3. **Copy `connect.py` + `setup_vps.ps1`** to the VPS — RDP clipboard/drag-drop,
   or `git clone` the repo there.
4. In an **admin PowerShell**: `Set-ExecutionPolicy -Scope Process Bypass -Force`
   then `.\setup_vps.ps1` (installs Python + the MetaTrader5 package).
5. Continue with the manual MT5 steps below.

## Setup

1. Download & install **MT5 from Admirals** (their branded terminal) on the
   chosen runtime.
2. Log into your **REAL Admirals account** by hand in the MT5 GUI. Note the
   exact **server name** shown in the login dialog (e.g. `Admirals-Live`).
3. (Only matters for the later execution step) enable **Algo Trading** in the
   toolbar.
4. Install the package where MT5 runs: `pip install -r requirements.txt`.
5. With the terminal open and logged in, run:

   ```
   python connect.py
   ```

   It prints whether the account is **REAL or DEMO**, balance/leverage/currency,
   the XAUUSD symbol specs (volume_min/step, contract size, live bid/ask), and
   any open positions — then exits. No orders.

If the gold symbol isn't found, the script lists the gold-like symbols on your
account; set `MT5_SYMBOL` to the right one and re-run.

## Credentials

The default flow needs **none** — log in via the GUI and the script attaches to
the running terminal. Only set `MT5_LOGIN/PASSWORD/SERVER` (copy `.env.example`
→ `.env`, which is gitignored) for unattended headless login on a VPS.

## Scope guard

- ✅ this session: connect + read (account, symbol, positions).
- ⛔ NOT this session: `order_send`, position sizing, SL/TP placement, the Go↔
  bridge boundary, going live. Those come after the real-account strategy is
  defined — and after the system clears its own go-live gates (PF > 1.6 over
  300+ clean paper trades; see project memory Phase 4.3).
