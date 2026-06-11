package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
)

func sampleAlert() GreenAlert {
	return GreenAlert{
		PendingID: 42,
		Time:      time.Date(2026, 6, 5, 14, 35, 0, 0, time.UTC),
		Symbol:    "XAUUSD",
		Interval:  "5m",
		Side:      core.Long,
		Strategy:  "OrderBlockMit",
		Entry:     2658.40,
		SL:        2654.10,
		TP:        2667.00,
		NetWeight: 2.24,
		Threshold: 1.95,
	}
}

func TestFormatGreen_ContainsKeyFields(t *testing.T) {
	msg := FormatGreen(sampleAlert())
	for _, want := range []string{"XAUUSD", "ENTRY (buena)", "LONG", "OrderBlockMit",
		"2658.40", "2654.10", "2667.00", "+2.24", "1.95", "5m", "14:35 UTC"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q\n--- got ---\n%s", want, msg)
		}
	}
}

func TestFormatGreen_DefaultsSymbolAndStrategy(t *testing.T) {
	a := sampleAlert()
	a.Symbol = ""
	a.Strategy = ""
	msg := FormatGreen(a)
	if !strings.Contains(msg, "XAUUSD") {
		t.Errorf("empty Symbol should default to XAUUSD, got: %s", msg)
	}
	if !strings.Contains(msg, "Composite") {
		t.Errorf("empty Strategy should default to Composite, got: %s", msg)
	}
}

func TestFormatGreen_TimeRenderedInUTC(t *testing.T) {
	a := sampleAlert()
	// Feed a non-UTC zone; the body must still print the UTC wall clock.
	loc := time.FixedZone("UTC-5", -5*3600)
	a.Time = time.Date(2026, 6, 5, 9, 35, 0, 0, loc) // == 14:35 UTC
	if msg := FormatGreen(a); !strings.Contains(msg, "14:35 UTC") {
		t.Errorf("time not normalized to UTC, got: %s", msg)
	}
}

func TestNotify_PostsExpectedPayload(t *testing.T) {
	var gotMethod, gotPath, gotCT, gotPhone, gotMessage string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotCT = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		var p struct{ Phone, Message string }
		_ = json.Unmarshal(body, &p)
		gotPhone, gotMessage = p.Phone, p.Message
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"success":true}`)
	}))
	defer srv.Close()

	wa := &WhatsApp{Endpoint: srv.URL + "/api/send", Phone: "573137054945", Client: srv.Client()}
	if err := wa.Notify(context.Background(), sampleAlert()); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/api/send" {
		t.Errorf("path = %s, want /api/send", gotPath)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %s, want application/json", gotCT)
	}
	if gotPhone != "573137054945" {
		t.Errorf("phone = %s, want 573137054945", gotPhone)
	}
	if !strings.Contains(gotMessage, "ENTRY (buena)") {
		t.Errorf("message body not the green alert: %s", gotMessage)
	}
}

func TestSend_PostsRawMessage(t *testing.T) {
	var gotPhone, gotMessage string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var p struct{ Phone, Message string }
		_ = json.Unmarshal(body, &p)
		gotPhone, gotMessage = p.Phone, p.Message
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	wa := &WhatsApp{Endpoint: srv.URL, Phone: "573137054945", Client: srv.Client()}
	if err := wa.Send(context.Background(), "hola desde la CLI"); err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if gotPhone != "573137054945" {
		t.Errorf("phone = %s, want 573137054945", gotPhone)
	}
	if gotMessage != "hola desde la CLI" {
		t.Errorf("message = %q, want raw passthrough (no green formatting)", gotMessage)
	}
}

func TestNotify_Non2xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"boom"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	wa := &WhatsApp{Endpoint: srv.URL, Phone: "573137054945", Client: srv.Client()}
	if err := wa.Notify(context.Background(), sampleAlert()); err == nil {
		t.Fatal("expected error on 500 response, got nil")
	}
}

func TestNotify_TransportErrorIsReturned(t *testing.T) {
	// Nothing listening on this endpoint → Client.Do fails.
	wa := &WhatsApp{
		Endpoint: "http://127.0.0.1:0/api/send",
		Phone:    "573137054945",
		Client:   &http.Client{Timeout: time.Second},
	}
	if err := wa.Notify(context.Background(), sampleAlert()); err == nil {
		t.Fatal("expected transport error, got nil")
	}
}

// Compile-time assertion that *WhatsApp satisfies the Notifier seam the
// paper-live loop depends on.
var _ Notifier = (*WhatsApp)(nil)
