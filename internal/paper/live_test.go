package paper

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
)

// captureLogger records formatted log lines for assertions.
type captureLogger struct{ lines []string }

func (c *captureLogger) Log(format string, args ...any) {
	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

// TestPostOrder_SendsBridgeContract: postOrder POSTs the exact JSON the MT5
// bridge expects (side/entry/sl/tp/strategy/key/risk_pct) and logs the outcome.
// This pins the wire contract between live.go and mt5bridge/server.py.
func TestPostOrder_SendsBridgeContract(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/order" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"dry_run":true,"lots":0.19,"reason":"DRY-RUN -- validated","ticket":null}`))
	}))
	defer srv.Close()

	log := &captureLogger{}
	postOrder(srv.URL+"/order", "SELL", 4275.0, 4283.0, 4255.0, 0.385,
		"SilverBullet", "SilverBullet|SHORT|4275|4283|4255", log)

	if got["side"] != "SELL" || got["strategy"] != "SilverBullet" {
		t.Fatalf("wrong side/strategy in body: %v", got)
	}
	if got["key"] != "SilverBullet|SHORT|4275|4283|4255" {
		t.Fatalf("wrong key in body: %v", got["key"])
	}
	if got["entry"].(float64) != 4275.0 || got["sl"].(float64) != 4283.0 ||
		got["tp"].(float64) != 4255.0 || got["risk_pct"].(float64) != 0.385 {
		t.Fatalf("wrong numeric values: %v", got)
	}
	if len(log.lines) != 1 || !strings.Contains(log.lines[0], "ok=true") {
		t.Fatalf("expected one outcome log with ok=true, got %v", log.lines)
	}
}

// TestSignalKeyIdentity: the structural identity collapses bars that share the
// same (Strategy, Side, Entry, SL, TP) anchor and separates ones that differ.
func TestSignalKeyIdentity(t *testing.T) {
	// Same anchor across two bars → same key (the real #75-#81 case:
	// SilverBullet SHORT 4319.80 / 4336.18 / 4288.55 recommended 7 bars running).
	a := signalKey("SilverBullet", core.Short.String(), 4319.80, 4336.18, 4288.55)
	b := signalKey("SilverBullet", core.Short.String(), 4319.80, 4336.18, 4288.55)
	if a != b {
		t.Fatalf("identical setups produced different keys: %q vs %q", a, b)
	}
	// Full-precision identity (the real rows were 4319.800985 to 6 decimals):
	// the same anchor recomputed yields bit-identical levels → same key.
	if hi := signalKey("SilverBullet", core.Short.String(), 4319.800985, 4336.17597, 4288.551014999999); hi !=
		signalKey("SilverBullet", core.Short.String(), 4319.800985, 4336.17597, 4288.551014999999) {
		t.Fatal("full-precision identity is not stable")
	}
	// Distinct level / side / strategy is a different setup — and we DON'T over-
	// merge: even a one-cent-apart entry stays distinct (no rounding collapse).
	for _, diff := range []string{
		signalKey("SilverBullet", core.Long.String(), 4319.80, 4336.18, 4288.55),   // side
		signalKey("SilverBullet", core.Short.String(), 4319.81, 4336.18, 4288.55),  // entry +1¢
		signalKey("OrderBlockMit", core.Short.String(), 4319.80, 4336.18, 4288.55), // strategy
	} {
		if diff == a {
			t.Fatalf("distinct setup collided with base key %q", a)
		}
	}
}

// TestDecideTradeAction_OneAtATime: while a trade is open, every new signal is
// held — no stacking — regardless of whether the setup was seen before.
func TestDecideTradeAction_OneAtATime(t *testing.T) {
	seen := map[string]bool{}
	key := signalKey("SilverBullet", core.Short.String(), 4319.80, 4336.18, 4288.55)
	if got := decideTradeAction(1, seen, key); got != actionHoldOpen {
		t.Fatalf("openCount=1 should HOLD, got %v", got)
	}
	// Even a brand-new distinct setup is held while one is open.
	other := signalKey("OrderBlockMit", core.Long.String(), 4000.00, 3990.00, 4020.00)
	if got := decideTradeAction(2, seen, other); got != actionHoldOpen {
		t.Fatalf("openCount=2 should HOLD any signal, got %v", got)
	}
}

// TestDecideTradeAction_EachSetupOnce: with the slot free, the first occurrence
// of a setup opens; every later occurrence of the SAME identity is deduped,
// while a different identity still opens. This is the 7→1 collapse.
func TestDecideTradeAction_EachSetupOnce(t *testing.T) {
	seen := map[string]bool{}
	key := signalKey("SilverBullet", core.Short.String(), 4319.80, 4336.18, 4288.55)

	// Replay the #75-#81 sequence: the same setup recommended on 7 bars, the
	// slot free each time (single position, fast SL). Mirrors what RunLive does
	// (insert → seen[key]=true) so we count exactly the inserts.
	inserts := 0
	for bar := range 7 {
		switch decideTradeAction(0, seen, key) {
		case actionInsert:
			inserts++
			seen[key] = true // RunLive marks the identity on a successful insert
		case actionDedup:
			// expected for bars 2..7
		case actionHoldOpen:
			t.Fatalf("bar %d: unexpected HOLD with slot free", bar)
		}
	}
	if inserts != 1 {
		t.Fatalf("each-setup-once failed: got %d trades for one structural setup, want 1", inserts)
	}

	// A genuinely different setup, slot still free, still opens.
	other := signalKey("OrderBlockMit", core.Long.String(), 4000.00, 3990.00, 4020.00)
	if got := decideTradeAction(0, seen, other); got != actionInsert {
		t.Fatalf("distinct unseen setup should INSERT, got %v", got)
	}
}

// TestDecideTradeAction_SeedSuppressesRestack: a setup seeded from recent live
// history (the restart case) is deduped on the first bar after a restart, so a
// still-on-chart anchor doesn't re-open under the new paper_run.
func TestDecideTradeAction_SeedSuppressesRestack(t *testing.T) {
	key := signalKey("SilverBullet", core.Short.String(), 4319.80, 4336.18, 4288.55)
	seeded := map[string]bool{key: true} // as loadRecentSignalKeys would return
	if got := decideTradeAction(0, seeded, key); got != actionDedup {
		t.Fatalf("seeded setup should DEDUP after restart, got %v", got)
	}
}
