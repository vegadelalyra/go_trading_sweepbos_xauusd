"""Pure position-sizing math for Admirals GOLD-T — no MetaTrader5 import, so it
is unit-testable on plain Linux python3.

Risk model (Daniel's plan): risk a fixed fraction of equity at the stop.
    risk_usd = equity * risk_pct
    raw_lots = risk_usd / (|entry - sl| * contract_size)
then floor to the broker's volume_step and clamp to [volume_min, volume_max].

contract_size for GOLD-T is 100 (1 lot = 100 oz), so a $1/oz move = $100/lot.
"""

import math


def round_to_step(vol: float, step: float, vmin: float, vmax: float) -> float:
    """Floor `vol` to a multiple of `step` (never round UP — never over-risk),
    then clamp into [vmin, vmax]. If the floored size is below vmin we return
    vmin (the smallest tradable size); the caller reports the resulting risk so
    an over-target min-lot is never silent."""
    if step <= 0:
        step = 0.01
    n = math.floor(vol / step + 1e-9)
    v = n * step
    if v < vmin:
        v = vmin
    if v > vmax:
        v = vmax
    # round to a sane number of decimals for lot sizes (steps are like 0.01)
    return round(v, 3)


def compute_lots(equity, risk_pct, entry, sl, contract_size, vmin, vstep, vmax):
    """Return (lots, actual_risk_usd, target_risk_usd, detail).

    actual_risk_usd is what the FINAL (rounded/clamped) lot size risks at the
    stop — it can differ from target when min-lot or step rounding bites, so the
    caller can see if the real exposure overshoots the intended risk_pct."""
    dist = abs(entry - sl)
    if dist <= 0:
        return 0.0, 0.0, 0.0, "SL distance is zero — refuse"
    if contract_size <= 0:
        return 0.0, 0.0, 0.0, "contract_size <= 0 — refuse"
    target = equity * risk_pct
    raw = target / (dist * contract_size)
    lots = round_to_step(raw, vstep, vmin, vmax)
    actual = lots * dist * contract_size
    detail = (f"target=${target:.2f} ({risk_pct*100:.1f}% of ${equity:.2f})  "
              f"dist=${dist:.2f}  raw={raw:.4f}  lots={lots}  "
              f"actual_risk=${actual:.2f} ({actual/equity*100:.1f}% of equity)")
    return lots, actual, target, detail
