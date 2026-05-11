package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/livefeed"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/meta"
)

// runLiveSignal implements the `live-signal` subcommand: subscribe to the
// Twelve Data WSS for current-price ticks, while a separate goroutine polls
// recently-closed candles via REST and recomputes the composite signal.
//
// The two streams are deliberately decoupled — ticks update the spot price
// shown in the dashboard line, candle closes drive the actual entry signal.
// Latency on ticks therefore matters (WSS); latency on candles does not
// (a poll every 30s is plenty for 15m setups).
func runLiveSignal(args []string) int {
	fs := flag.NewFlagSet("live-signal", flag.ContinueOnError)
	runID := fs.Int64("run-id", 0, "backtest_runs.id to source weights (0 = latest)")
	symbol := fs.String("symbol", "XAU/USD", "symbol")
	interval := fs.String("interval", "15m", "candle interval for the signal engine")
	threshold := fs.Float64("threshold", 2.0, "composite |NetWeight| threshold")
	pollSeconds := fs.Int("poll-seconds", 30, "REST candle poll cadence")
	bySession := fs.Bool("by-session", false, "use per-session weights")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	apiKey := os.Getenv("TWELVEDATA_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "ERROR: define TWELVEDATA_API_KEY")
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool, err := openPool(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		return 1
	}
	defer pool.Close()

	id := *runID
	if id == 0 {
		if err := pool.QueryRow(ctx, `SELECT id FROM backtest_runs ORDER BY id DESC LIMIT 1`).Scan(&id); err != nil {
			fmt.Fprintf(os.Stderr, "find latest run: %v\n", err)
			return 1
		}
	}

	// Resolve composer ONCE — the polled goroutine reuses it on every tick.
	var compose func(candles []Candle, a Analysis, i int) meta.CompositeSignal
	if *bySession {
		ws, err := meta.LoadWeightsBySession(ctx, pool, id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load weights-by-session: %v\n", err)
			return 1
		}
		compose = func(c []Candle, a Analysis, i int) meta.CompositeSignal {
			return meta.ComposeSignalBySession(c, a, i, ws, *threshold)
		}
	} else {
		ws, err := meta.LoadWeights(ctx, pool, id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load weights: %v\n", err)
			return 1
		}
		wm := meta.WeightMap(ws)
		compose = func(c []Candle, a Analysis, i int) meta.CompositeSignal {
			return meta.ComposeSignal(c, a, i, wm, *threshold)
		}
	}

	// Subscribe to the live tick stream. Failure is non-fatal — the signal
	// loop can still operate on REST polls.
	feed := livefeed.New(apiKey, []string{*symbol})
	if err := feed.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "warn: live feed unavailable (%v) — running with REST polls only\n", err)
	}
	defer feed.Close()

	fmt.Printf("═══ Live Signal — %s %s ═══\n", *symbol, *interval)
	fmt.Printf("  Run #%d  threshold=%.2f  poll=%ds  bySession=%t\n\n",
		id, *threshold, *pollSeconds, *bySession)

	go printTicks(ctx, feed)

	ticker := time.NewTicker(time.Duration(*pollSeconds) * time.Second)
	defer ticker.Stop()

	var lastRecommendedAt time.Time
	pollOnce := func() {
		candles, err := loadCandles(ctx, pool, *symbol, *interval, time.Time{}, time.Time{})
		if err != nil {
			fmt.Fprintf(os.Stderr, "load candles: %v\n", err)
			return
		}
		if len(candles) < 200 {
			fmt.Fprintf(os.Stderr, "not enough candles (%d) — run backfill\n", len(candles))
			return
		}
		a := Analyze(candles)
		cs := compose(candles, a, len(candles)-1)
		now := candles[len(candles)-1].Time
		if cs.Recommended && !now.Equal(lastRecommendedAt) {
			fmt.Printf("[%s] %s  entry=%.2f  SL=%.2f  TP=%.2f  net=%+.2f  conf=%.0f%%\n",
				now.Format("15:04"), cs.Side.String(),
				cs.BestEntry, cs.BestSL, cs.BestTP, cs.NetWeight, cs.Confidence*100)
			lastRecommendedAt = now
		}
	}
	pollOnce()

	for {
		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
			pollOnce()
		}
	}
}

// printTicks streams live ticks to stderr — purely informational, the price
// updates do not feed the composite engine (which is candle-based).
func printTicks(ctx context.Context, feed *livefeed.Feed) {
	for {
		select {
		case <-ctx.Done():
			return
		case tick, ok := <-feed.Ticks():
			if !ok {
				return
			}
			fmt.Fprintf(os.Stderr, "\rspot %s $%.2f @ %s   ",
				tick.Symbol, tick.Price, tick.Time.Format("15:04:05"))
		}
	}
}
