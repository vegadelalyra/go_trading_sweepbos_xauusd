// Package livefeed talks to Twelve Data's WebSocket quote stream and emits
// real-time price ticks on a channel. The package is intentionally narrow —
// it does not interpret ticks (no candle aggregation, no signal logic),
// it just hands the consumer a chronological stream they can process.
//
// Wiring expectation: the CLI / signal driver listens on Ticks() for the
// "current price" display while a separate goroutine polls REST for closed
// candles and feeds those into ComposeSignal. The two streams share nothing
// — ticks update the UI; candle closes drive the decisions.
package livefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// TDEndpoint is the Twelve Data WSS quote endpoint. Requires a paid API key.
const TDEndpoint = "wss://ws.twelvedata.com/v1/quotes/price"

// Tick carries one quote event from the upstream stream.
type Tick struct {
	Symbol string
	Price  float64
	Time   time.Time
}

// Feed manages a single WSS subscription. Start opens the connection,
// subscribes to the given symbols and begins forwarding events. Close
// terminates the read loop and the ticks channel.
type Feed struct {
	apiKey  string
	symbols []string
	conn    *websocket.Conn
	ticks   chan Tick
	errCh   chan error
	once    sync.Once
}

// New constructs a Feed bound to apiKey and the given symbols. Call Start
// to actually open the WSS connection.
func New(apiKey string, symbols []string) *Feed {
	return &Feed{
		apiKey:  apiKey,
		symbols: symbols,
		ticks:   make(chan Tick, 256),
		errCh:   make(chan error, 1),
	}
}

// Start opens the WSS connection, subscribes to the configured symbols and
// spawns the read loop. The context controls the lifetime of the read loop;
// cancelling ctx (or calling Close) drops the connection.
func (f *Feed) Start(ctx context.Context) error {
	u, err := url.Parse(TDEndpoint)
	if err != nil {
		return fmt.Errorf("parse endpoint: %w", err)
	}
	q := u.Query()
	q.Set("apikey", f.apiKey)
	u.RawQuery = q.Encode()

	c, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial WSS: %w", err)
	}
	f.conn = c

	sub := map[string]any{
		"action": "subscribe",
		"params": map[string]any{"symbols": f.symbols},
	}
	if err := c.WriteJSON(sub); err != nil {
		_ = c.Close()
		return fmt.Errorf("send subscribe: %w", err)
	}
	go f.readLoop(ctx)
	return nil
}

// Ticks returns the channel of incoming price events. The channel closes
// when the feed terminates (context cancellation, network error, Close).
func (f *Feed) Ticks() <-chan Tick { return f.ticks }

// Err returns the first non-nil error from the read loop. Read after the
// ticks channel closes to learn why the feed stopped.
func (f *Feed) Err() error {
	select {
	case err := <-f.errCh:
		return err
	default:
		return nil
	}
}

// Close drops the WSS connection. Idempotent.
func (f *Feed) Close() error {
	if f.conn == nil {
		return nil
	}
	return f.conn.Close()
}

func (f *Feed) readLoop(ctx context.Context) {
	defer f.once.Do(func() { close(f.ticks) })

	type msg struct {
		Event     string  `json:"event"`
		Symbol    string  `json:"symbol"`
		Price     float64 `json:"price"`
		Timestamp int64   `json:"timestamp"`
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		_, raw, err := f.conn.ReadMessage()
		if err != nil {
			select {
			case f.errCh <- fmt.Errorf("read: %w", err):
			default:
			}
			return
		}
		var m msg
		if err := json.Unmarshal(raw, &m); err != nil {
			// Subscribe-ack and heartbeat frames don't fit the price
			// schema. Skip silently — only price events matter.
			continue
		}
		if m.Event != "price" {
			continue
		}
		t := time.Unix(m.Timestamp, 0).UTC()
		select {
		case f.ticks <- Tick{Symbol: m.Symbol, Price: m.Price, Time: t}:
		case <-ctx.Done():
			return
		}
	}
}
