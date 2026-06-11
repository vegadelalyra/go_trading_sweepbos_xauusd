# mt5bridge/setup_vps.ps1 — one-shot Python + MetaTrader5 setup for a Windows VPS.
#
# Installs Python and the MetaTrader5 package. It does NOT install the MT5
# terminal, does NOT log in, and places NO orders — those are manual/deliberate.
#
# Run in an ADMIN PowerShell on the VPS:
#   Set-ExecutionPolicy -Scope Process Bypass -Force
#   .\setup_vps.ps1

$ErrorActionPreference = "Stop"
function Have($cmd) { $null -ne (Get-Command $cmd -ErrorAction SilentlyContinue) }

# ── 1. Python ────────────────────────────────────────────────────────────
if (-not (Have python)) {
    Write-Host "Installing Python 3.12..."
    if (Have winget) {
        winget install -e --id Python.Python.3.12 --accept-source-agreements --accept-package-agreements
    } else {
        # Windows Server without winget: silent install from python.org.
        $url = "https://www.python.org/ftp/python/3.12.4/python-3.12.4-amd64.exe"
        $out = "$env:TEMP\python-install.exe"
        Invoke-WebRequest -Uri $url -OutFile $out
        Start-Process -Wait $out -ArgumentList "/quiet InstallAllUsers=1 PrependPath=1"
    }
    Write-Host ""
    Write-Host "Python installed. CLOSE and REOPEN PowerShell (so PATH refreshes), then re-run this script."
    exit 0
}
python --version

# ── 2. MetaTrader5 package ───────────────────────────────────────────────
python -m pip install --upgrade pip
python -m pip install --upgrade MetaTrader5
python -c "import MetaTrader5 as m; print('MetaTrader5 OK', m.__version__)"

Write-Host ""
Write-Host "── Next (manual) ─────────────────────────────────────────────"
Write-Host " 1. Download MT5 from Admirals' website; install it."
Write-Host " 2. Log into your REAL Admirals account in the MT5 GUI; note the exact server name."
Write-Host " 3. Toolbar -> enable 'Algo Trading' (only matters for the later execution step)."
Write-Host " 4. With the terminal open & logged in, from this folder:  python connect.py"
Write-Host ""
Write-Host "connect.py is READ-ONLY (no order_send). It prints REAL-vs-DEMO, balance, XAUUSD specs, positions."
