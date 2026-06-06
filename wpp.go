package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/notify"
)

// runWppSend sends a one-off WhatsApp message to a number via a running
// go_wpp_bot instance (POST /api/send). The message may be passed as
// positional args (joined with spaces) or via -message. Pure transport — no DB,
// no TwelveData; safe to run any time the bot is up and QR-paired.
//
//	sweepbos wpp-send "hola desde la CLI"
//	sweepbos wpp-send -phone 573137054945 -message "texto con flags"
func runWppSend(args []string) int {
	fs := flag.NewFlagSet("wpp-send", flag.ContinueOnError)
	phone := fs.String("phone", "573137054945", "destination WhatsApp number (country code, no + or @suffix)")
	endpoint := fs.String("endpoint", "http://127.0.0.1:8080/api/send", "go_wpp_bot send endpoint (127.0.0.1 forces IPv4 to match the bot's *:8080 bind)")
	message := fs.String("message", "", "message text; if empty, the positional args are joined as the message")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	msg := *message
	if msg == "" {
		msg = strings.Join(fs.Args(), " ")
	}
	if strings.TrimSpace(msg) == "" {
		fmt.Fprintln(os.Stderr, "wpp-send: empty message")
		fmt.Fprintln(os.Stderr, `  e.g. sweepbos wpp-send "hola desde la CLI"`)
		return 2
	}

	wa := &notify.WhatsApp{
		Endpoint: *endpoint,
		Phone:    *phone,
		Client:   &http.Client{Timeout: 10 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := wa.Send(ctx, msg); err != nil {
		fmt.Fprintf(os.Stderr, "wpp-send: %v\n", err)
		fmt.Fprintf(os.Stderr, "  is go_wpp_bot running and QR-paired? probe: curl -s -o /dev/null -w '%%{http_code}' -X POST %s -d '{}'\n", *endpoint)
		return 1
	}
	fmt.Printf("✅ sent to %s\n", *phone)
	return 0
}
