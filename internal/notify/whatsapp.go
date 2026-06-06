// Package notify pushes high-conviction trading alerts to out-of-band
// channels. The only implementation today is WhatsApp via a running
// go_wpp_bot instance, but the Notifier seam keeps the paper-live loop
// agnostic to the transport — paper.LiveConfig holds a notify.Notifier, not
// an HTTP client.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
)

// GreenAlert is a strong-conviction signal (ENTRY tier, |NetWeight| ≥ 2.0)
// worth pushing to the operator's phone the instant it fires. It carries the
// same numbers the CLI logger prints so the WhatsApp body is self-contained.
type GreenAlert struct {
	PendingID int64
	Time      time.Time // bar time (UTC)
	Symbol    string
	Interval  string
	Side      core.Side
	Strategy  string
	Entry     float64
	SL        float64
	TP        float64
	NetWeight float64
	Threshold float64
}

// Notifier delivers a GreenAlert to some external channel. The paper-live
// loop calls Notify from a goroutine, so implementations may block on I/O;
// they must honour ctx cancellation and must not panic. A nil Notifier means
// notifications are disabled — callers guard for it.
type Notifier interface {
	Notify(ctx context.Context, a GreenAlert) error
}

// WhatsApp posts green alerts to a running go_wpp_bot instance through its
// POST /api/send endpoint (body {"phone","message"}, no auth, localhost-only).
// The send path in go_wpp_bot is not importable (package-global client + a
// SQLite lock it owns), so we integrate over HTTP — which also keeps the
// bot's lifecycle fully decoupled from the trader binary.
type WhatsApp struct {
	Endpoint string       // e.g. "http://localhost:8080/api/send"
	Phone    string       // e.g. "573137054945" — go_wpp_bot appends @s.whatsapp.net
	Client   *http.Client // nil → a private client with a 10s timeout
}

// Notify renders a green alert and sends it. Thin wrapper over Send so the
// live loop deals in typed GreenAlerts while Send owns the transport. A non-2xx
// response or transport error propagates up, where the loop logs it without
// aborting.
func (w *WhatsApp) Notify(ctx context.Context, a GreenAlert) error {
	return w.Send(ctx, FormatGreen(a))
}

// Send POSTs a raw text message to the bot's /api/send endpoint. Shared by
// Notify (green alerts) and the `wpp-send` CLI subcommand (manual sends).
func (w *WhatsApp) Send(ctx context.Context, message string) error {
	payload, err := json.Marshal(map[string]string{
		"phone":   w.Phone,
		"message": message,
	})
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("post %s: %w", w.Endpoint, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("wpp bot %s returned %s", w.Endpoint, resp.Status)
	}
	return nil
}

// FormatGreen builds the WhatsApp message body. Split out from Notify so it
// can be asserted in tests without a live endpoint. Kept compact — it has to
// read well on a phone lock-screen.
func FormatGreen(a GreenAlert) string {
	symbol := a.Symbol
	if symbol == "" {
		symbol = "XAUUSD"
	}
	strat := a.Strategy
	if strat == "" {
		strat = "Composite"
	}
	return fmt.Sprintf(
		"🟢 %s ENTRY (buena)\n%s · %s\nentry %.2f  SL %.2f  TP %.2f\nnet %+.2f  vote %.2f  ·  %s  ·  %s UTC",
		symbol, a.Side.String(), strat,
		a.Entry, a.SL, a.TP,
		a.NetWeight, a.Threshold, a.Interval,
		a.Time.UTC().Format("15:04"),
	)
}
