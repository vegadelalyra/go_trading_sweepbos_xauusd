"""Run on plain Linux: python3 test_bridgelib.py  (no MetaTrader5 needed)."""
import os
import tempfile

from bridgelib import signal_key, append_order, load_recent_keys, format_fill_message


def test_signal_key():
    assert signal_key("SilverBullet", "SELL", 4319.8, 4336.18, 4288.55) == \
        "SilverBullet|SELL|4319.8|4336.18|4288.55"


def test_log_roundtrip_window_and_event_filter():
    fd, path = tempfile.mkstemp()
    os.close(fd)
    try:
        now = 1_000_000
        append_order(path, {"event": "placed", "key": "A", "ts": now})         # recent
        append_order(path, {"event": "placed", "key": "B", "ts": now - 100_000})  # too old
        append_order(path, {"event": "dryrun", "key": "C", "ts": now})         # not a real placement
        append_order(path, {"event": "fill", "key": "D", "ts": now})           # fills don't seed dedup
        keys = load_recent_keys(path, 86_400, now)
        assert keys == {"A"}, keys
    finally:
        os.remove(path)


def test_load_missing_file_is_empty():
    assert load_recent_keys("/nonexistent/path/orders.log", 86_400, 1) == set()


def test_format_fill_message_real_case():
    msg = format_fill_message({
        "kind": "REAL", "side": "SELL", "symbol": "GOLD-T", "strategy": "SilverBullet",
        "price_open": 4319.80, "volume": 0.09, "sl": 4336.18, "tp": 4288.55,
        "loss_at_sl": 0.09 * 16.38 * 100, "gain_at_tp": 0.09 * 31.25 * 100,
        "margin": 39.10, "equity": 414.57, "free": 375.47, "ticket": 12345678,
    })
    assert msg.isascii(), "WhatsApp/wine stdout must stay ASCII"
    for token in ("OPERACION ABIERTA (REAL)", "SELL GOLD-T", "SilverBullet",
                  "lots=0.09", "SL=4336.18", "TP=4288.55", "perdida@SL=$147.42",
                  "equity=$414.57", "ticket=12345678"):
        assert token in msg, f"missing {token!r} in:\n{msg}"


def test_format_fill_message_no_tp():
    msg = format_fill_message({
        "kind": "DEMO", "side": "BUY", "symbol": "GOLD-T", "strategy": None,
        "price_open": 4300.0, "volume": 0.01, "sl": 4290.0, "tp": 0.0,
        "loss_at_sl": 0.01 * 10 * 100, "gain_at_tp": 0.0,
        "margin": 4.3, "equity": 414.0, "free": 410.0, "ticket": 999,
    })
    assert "TP=none" in msg
    assert "ganancia@TP" not in msg   # no TP -> no gain line
    assert "  ?" in msg               # missing strategy renders as '?'


if __name__ == "__main__":
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            fn()
            print(f"PASS {name}")
    print("all bridgelib tests passed")
