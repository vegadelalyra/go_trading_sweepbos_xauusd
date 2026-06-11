"""Run on plain Linux: python3 test_bridgelib.py  (no MetaTrader5 needed)."""
import os
import tempfile

from bridgelib import (signal_key, append_order, load_recent_keys, format_fill_message,
                       is_transient_retcode, retcode_name, send_with_retry,
                       format_reject_message, format_expiry_message, format_watchdog_message)


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


# ── resilience: retcode classifier + send_with_retry + alert formatters ──────

class _Res:
    def __init__(self, retcode):
        self.retcode = retcode


def _transient(r):
    return True if r is None else is_transient_retcode(getattr(r, "retcode", None))


def test_is_transient_retcode():
    for rc in (10031, 10004, 10021):
        assert is_transient_retcode(rc), rc
    for rc in (10009, 0, 10015, 10016, 10018, 10019, 10027, None, "x"):
        assert not is_transient_retcode(rc), rc


def test_retcode_name():
    assert retcode_name(10031) == "CONNECTION"
    assert retcode_name(10009) == "DONE"
    assert retcode_name(123456) == "123456"


def test_send_with_retry_transient_then_success():
    seq = [_Res(10031), _Res(10004), _Res(10009)]  # 2 transient, then DONE
    i = {"n": 0}

    def send():
        r = seq[i["n"]]
        i["n"] += 1
        return r

    res, tries, retries = send_with_retry(send, _transient, 10009, attempts=5,
                                          backoff=0.0, max_sec=999,
                                          sleep=lambda *_: None, clock=lambda: 0.0)
    assert res.retcode == 10009
    assert tries == 3, tries
    assert len(retries) == 2


def test_send_with_retry_terminal_no_retry():
    res, tries, retries = send_with_retry(lambda: _Res(10019), _transient, 10009,
                                          attempts=5, backoff=0.0,
                                          sleep=lambda *_: None, clock=lambda: 0.0)
    assert res.retcode == 10019
    assert tries == 1, tries
    assert retries == []


def test_send_with_retry_exhausts_attempts():
    res, tries, _ = send_with_retry(lambda: _Res(10031), _transient, 10009,
                                    attempts=3, backoff=0.0,
                                    sleep=lambda *_: None, clock=lambda: 0.0)
    assert res.retcode == 10031
    assert tries == 3, tries


def test_send_with_retry_none_is_transient_and_time_bounded():
    ticks = iter([0.0, 100.0, 200.0])
    res, tries, _ = send_with_retry(lambda: None, _transient, 10009, attempts=10,
                                    backoff=4.0, max_sec=20.0,
                                    sleep=lambda *_: None, clock=lambda: next(ticks))
    assert res is None
    assert tries == 1, tries  # None is transient, but the time budget blew at once


def test_format_reject_message():
    msg = format_reject_message({
        "kind": "REAL", "side": "BUY", "symbol": "GOLD-T", "strategy": "OrderBlockMit",
        "entry": 4070.0, "sl": 4066.0, "tp": 4082.0, "retcode": 10031, "attempts": 3,
        "reason": "order_send retcode=10031 (CONNECTION): no connection"})
    assert msg.isascii()
    for t in ("ORDEN RECHAZADA (REAL)", "BUY GOLD-T", "OrderBlockMit",
              "retcode=10031 (CONNECTION)", "intentos=3"):
        assert t in msg, (t, msg)


def test_format_expiry_message():
    msg = format_expiry_message({"side": "BUY", "symbol": "GOLD-T", "strategy": "SilverBullet",
                                 "entry": 4070.0, "sl": 4066.0, "tp": 4082.0, "ticket": 555})
    assert msg.isascii()
    for t in ("LIMIT EXPIRADA SIN LLENAR", "BUY GOLD-T", "ticket=555", "15min"):
        assert t in msg, (t, msg)


def test_format_watchdog_message():
    down = format_watchdog_message(False, "bridge unreachable")
    up = format_watchdog_message(True, "OK equity=$380")
    assert down.startswith("WATCHDOG: ejecucion CAIDA")
    assert up.startswith("WATCHDOG: ejecucion RECUPERADA")
    assert down.isascii() and up.isascii()


if __name__ == "__main__":
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            fn()
            print(f"PASS {name}")
    print("all bridgelib tests passed")
