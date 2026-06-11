"""Run on plain Linux: python3 test_sizing.py"""
from sizing import compute_lots, round_to_step


def approx(a, b, tol=1e-6):
    return abs(a - b) <= tol


def test_round_floor_never_up():
    # 0.099 with step 0.01 floors to 0.09 (never 0.10 — never over-risk)
    assert round_to_step(0.099, 0.01, 0.01, 25.0) == 0.09
    assert round_to_step(0.0099, 0.01, 0.01, 25.0) == 0.01   # below min -> min
    assert round_to_step(999, 0.01, 0.01, 25.0) == 25.0      # clamp to max


def test_real_account_green():
    # The real #75 case: SilverBullet SHORT entry 4319.80 SL 4336.18 on the
    # verified account (equity ~414.57, GOLD-T contract 100, min/step 0.01).
    lots, actual, target, _ = compute_lots(
        equity=414.57, risk_pct=0.385,
        entry=4319.80, sl=4336.18,
        contract_size=100.0, vmin=0.01, vstep=0.01, vmax=25.0)
    # target = 414.57 * 0.385 = 159.61 ; dist = 16.38 ; raw = 159.61/1638 = 0.0975
    assert approx(target, 159.6094, 1e-3), target
    assert lots == 0.09, lots                       # floored from 0.0975
    assert approx(actual, 0.09 * 16.38 * 100), actual  # = 147.42
    assert actual <= target                         # floor never overshoots here


def test_zero_distance_refused():
    lots, *_ = compute_lots(414, 0.385, 4300, 4300, 100, 0.01, 0.01, 25)
    assert lots == 0.0


def test_wide_sl_minlot_overshoot_is_reported():
    # SL so wide that even 0.01 lot risks > target -> we still return min lot
    # but actual_risk is reported ABOVE target so it's never silent.
    lots, actual, target, _ = compute_lots(
        equity=414.57, risk_pct=0.385,
        entry=4300, sl=4100,   # dist 200 -> 0.01 lot risks $200
        contract_size=100.0, vmin=0.01, vstep=0.01, vmax=25.0)
    assert lots == 0.01
    assert actual > target     # 200 > 159.6 — caller can see the overshoot


if __name__ == "__main__":
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            fn()
            print(f"PASS {name}")
    print("all sizing tests passed")
