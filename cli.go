package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/joho/godotenv"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/xasset"
)

// run is the CLI dispatcher invoked from main().
func run(args []string) int {
	_ = godotenv.Load()

	if len(args) < 2 {
		return runTUI()
	}

	switch args[1] {
	case "tui":
		return runTUI()
	case "backfill":
		return runBackfill(args[2:])
	case "coverage":
		return runCoverage(args[2:])
	case "backtest":
		return runBacktestCLI(args[2:])
	case "signal":
		return runSignal(args[2:])
	case "weights":
		return runWeights(args[2:])
	case "wfa":
		return runWFA(args[2:])
	case "import-cache":
		return runImportCache(args[2:])
	case "-h", "--help", "help":
		printUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %q\n\n", args[1])
		printUsage()
		return 2
	}
}

func printUsage() {
	fmt.Println(`go_trading_sweepbos_xauusd — XAUUSD smart-money backtester

USAGE
  go_trading_sweepbos_xauusd [SUBCOMMAND] [FLAGS]

SUBCOMMANDS
  tui           (default) run the interactive TUI
  backfill      download historical candles from TwelveData into Postgres
  coverage      print how many candles we have per (symbol, interval)
  backtest      run the backtest against cached candles and print bucket stats
  signal        compose a live signal using strategy weights from a past run
  weights       print the per-strategy weights derived from a past run
  wfa           walk-forward analysis: rolling IS→OOS windows
  import-cache  one-shot import of the legacy JSON cache into Postgres

FLAGS for `+"`backfill`"+`
  -symbol      default XAU/USD
  -interval    "15m", "1h", "4h", or "all" (runs all three).  Default: all
  -years       how many years back to fetch.                  Default: 2
  -pages       safety cap on TD pages per interval.           Default: 50

FLAGS for `+"`import-cache`"+`
  -file        path to legacy cache_xauusd_15m.json.          Default: ./cache_xauusd_15m.json
  -symbol      default XAU/USD
  -interval    default 15m

ENVIRONMENT
  TWELVEDATA_API_KEY     required for live, historic, and backfill
  CANDLES_DSN            Postgres DSN; default:
                         postgres://emy_admin:emy-1234@127.0.0.1:5432/candles?sslmode=disable
`)
}

func runTUI() int {
	apiKey := os.Getenv("TWELVEDATA_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "ERROR: define TWELVEDATA_API_KEY en .env")
		return 1
	}
	ctx := context.Background()
	pool, err := openPool(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		return 1
	}
	defer pool.Close()

	p := tea.NewProgram(newModel(apiKey, pool), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "run: %v\n", err)
		return 1
	}
	return 0
}

func runBackfill(args []string) int {
	fs := flag.NewFlagSet("backfill", flag.ContinueOnError)
	symbol := fs.String("symbol", "XAU/USD", "symbol to fetch")
	interval := fs.String("interval", "all", `"15m" | "1h" | "4h" | "all"`)
	years := fs.Int("years", 2, "years of history to fetch")
	pages := fs.Int("pages", 50, "max pages per interval (safety cap)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	apiKey := os.Getenv("TWELVEDATA_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "ERROR: TWELVEDATA_API_KEY is required")
		return 1
	}

	intervals := []string{*interval}
	if *interval == "all" {
		intervals = []string{"15m", "1h", "4h"}
	}

	ctx := context.Background()
	pool, err := openPool(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		return 1
	}
	defer pool.Close()

	httpc := &http.Client{Timeout: 60 * time.Second}

	for _, iv := range intervals {
		cfg := defaultBackfillCfg(*symbol, iv, *years)
		cfg.MaxPages = *pages
		fmt.Printf("\n=== backfill %s %s (target: %s) ===\n",
			*symbol, iv, cfg.TargetStart.Format("2006-01-02"))
		inserted, err := backfill(ctx, pool, httpc, apiKey, cfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[%s] error: %v (inserted=%d before error)\n", iv, err, inserted)
			continue
		}
		fmt.Printf("[%s] DONE — inserted %d new candles\n", iv, inserted)
	}
	return 0
}

func runCoverage(args []string) int {
	fs := flag.NewFlagSet("coverage", flag.ContinueOnError)
	symbol := fs.String("symbol", "XAU/USD", "symbol")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx := context.Background()
	pool, err := openPool(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		return 1
	}
	defer pool.Close()

	rows, err := coverage(ctx, pool, *symbol)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query: %v\n", err)
		return 1
	}
	if len(rows) == 0 {
		fmt.Printf("no candles in DB for %s\n", *symbol)
		return 0
	}
	fmt.Printf("\nCoverage for %s\n", *symbol)
	fmt.Printf("%-8s  %-8s  %-19s  %-19s  %-7s\n", "interval", "count", "oldest", "newest", "span")
	fmt.Println(strings.Repeat("-", 70))
	for _, r := range rows {
		span := r.Newest.Sub(r.Oldest)
		days := int(span.Hours() / 24)
		fmt.Printf("%-8s  %-8d  %-19s  %-19s  %dd\n",
			r.Interval, r.Count,
			r.Oldest.Format("2006-01-02 15:04"),
			r.Newest.Format("2006-01-02 15:04"),
			days)
	}
	fmt.Println()
	return 0
}

func runBacktestCLI(args []string) int {
	fs := flag.NewFlagSet("backtest", flag.ContinueOnError)
	symbol := fs.String("symbol", "XAU/USD", "symbol")
	interval := fs.String("interval", "15m", "interval to backtest on")
	fromStr := fs.String("from", "", "lower bound (YYYY-MM-DD); empty = all history")
	toStr := fs.String("to", "", "upper bound (YYYY-MM-DD); empty = up to now")
	persist := fs.Bool("persist", false, "store this run + every trade in backtest_runs/backtest_trades")
	useBias := fs.Bool("bias", false, "filter signals against H4 EMA200 trend bias")
	h4Period := fs.Int("h4-period", 200, "EMA period for the H4 bias filter")
	atrMult := fs.Float64("atr-mult", 0, "if >0, widen SL to max(structural, ATR×mult)")
	atrPeriod := fs.Int("atr-period", 14, "ATR period when -atr-mult is set")
	useXAG := fs.Bool("xag", false, "load XAG/USD candles for the XAUXAGPairs strategy")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx := context.Background()
	pool, err := openPool(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		return 1
	}
	defer pool.Close()

	var from, to time.Time
	if *fromStr != "" {
		t, err := time.Parse("2006-01-02", *fromStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "from: %v\n", err)
			return 2
		}
		from = t
	}
	if *toStr != "" {
		t, err := time.Parse("2006-01-02", *toStr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "to: %v\n", err)
			return 2
		}
		to = t.Add(24 * time.Hour)
	}

	candles, err := loadCandles(ctx, pool, *symbol, *interval, from, to)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load: %v\n", err)
		return 1
	}
	if len(candles) < 100 {
		fmt.Fprintf(os.Stderr, "only %d candles loaded — run `backfill` first\n", len(candles))
		return 1
	}

	fmt.Printf("Loaded %d candles  range=%s → %s\n",
		len(candles),
		candles[0].Time.Format("2006-01-02 15:04"),
		candles[len(candles)-1].Time.Format("2006-01-02 15:04"))

	params := ParamsFor(*interval)
	if *useBias {
		h4, err := loadCandles(ctx, pool, *symbol, "4h", from, to)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load h4: %v\n", err)
			return 1
		}
		if len(h4) < *h4Period+10 {
			fmt.Fprintf(os.Stderr, "not enough H4 candles (%d) for bias period %d\n", len(h4), *h4Period)
		} else {
			params.H4Bias = BuildBiasMap(h4, *h4Period, 4*time.Hour)
			fmt.Printf("Bias filter ON  (H4 EMA%d, %d bars)\n", *h4Period, len(h4))
		}
	}
	if *atrMult > 0 {
		params.ATRMult = *atrMult
		params.ATRPeriod = *atrPeriod
		fmt.Printf("ATR SL widening ON  (mult=%.2f period=%d)\n", *atrMult, *atrPeriod)
	}
	if *useXAG {
		xag, err := loadCandles(ctx, pool, "XAG/USD", "1h", from, to)
		if err != nil || len(xag) == 0 {
			fmt.Fprintf(os.Stderr, "XAG data unavailable — XAUXAGPairs will stay inactive\n")
		} else {
			xasset.XAUXAGPairsInstance.SetXAG(xag)
			fmt.Printf("XAG candles loaded (%d 1h bars) — XAUXAGPairs active\n", len(xag))
		}
	}
	result := RunBacktest(candles, Strategies, params)
	printBacktestReport(*symbol, *interval, result)

	if *persist {
		runID, err := saveBacktestRun(ctx, pool, *symbol, *interval, candles, result)
		if err != nil {
			fmt.Fprintf(os.Stderr, "persist: %v\n", err)
			return 1
		}
		fmt.Printf("\nPersisted as backtest_runs.id=%d with %d trades\n", runID, len(result.Trades))
	}
	return 0
}

func runWeights(args []string) int {
	fs := flag.NewFlagSet("weights", flag.ContinueOnError)
	runID := fs.Int64("run-id", 0, "backtest_runs.id to source weights from (0 = latest)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx := context.Background()
	pool, err := openPool(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		return 1
	}
	defer pool.Close()

	id := *runID
	if id == 0 {
		err = pool.QueryRow(ctx, `SELECT id FROM backtest_runs ORDER BY id DESC LIMIT 1`).Scan(&id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "find latest run: %v\n", err)
			return 1
		}
	}
	ws, err := LoadWeights(ctx, pool, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load weights: %v\n", err)
		return 1
	}
	fmt.Printf("\nWeights derived from backtest_runs.id=%d\n\n", id)
	fmt.Println(FormatWeights(ws))
	return 0
}

func runSignal(args []string) int {
	fs := flag.NewFlagSet("signal", flag.ContinueOnError)
	runID := fs.Int64("run-id", 0, "backtest_runs.id to source weights (0 = latest)")
	symbol := fs.String("symbol", "XAU/USD", "symbol")
	interval := fs.String("interval", "15m", "interval to evaluate")
	threshold := fs.Float64("threshold", 2.0, "min |net weight| required to recommend")
	capital := fs.Float64("capital", 0, "USD to risk; if >0, compute Admirals units & margin")
	scanBack := fs.Int("scan-back", 0, "also scan the last N candles for any recommended signals")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx := context.Background()
	pool, err := openPool(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		return 1
	}
	defer pool.Close()

	id := *runID
	if id == 0 {
		err = pool.QueryRow(ctx, `SELECT id FROM backtest_runs ORDER BY id DESC LIMIT 1`).Scan(&id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "find latest run: %v\n", err)
			return 1
		}
	}
	ws, err := LoadWeights(ctx, pool, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load weights: %v\n", err)
		return 1
	}
	weights := WeightMap(ws)

	candles, err := loadCandles(ctx, pool, *symbol, *interval, time.Time{}, time.Time{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "load candles: %v\n", err)
		return 1
	}
	if len(candles) < 200 {
		fmt.Fprintf(os.Stderr, "not enough candles (%d) — run backfill first\n", len(candles))
		return 1
	}
	a := Analyze(candles)

	// First, always report the current candle.
	cs := ComposeSignal(candles, a, len(candles)-1, weights, *threshold)
	printCompositeSignal(*symbol, *interval, candles[len(candles)-1], cs, id, *threshold, *capital)

	// Optional: walk back N candles looking for the most recent
	// recommended (threshold-crossing) signal — useful when "now" is quiet.
	if *scanBack > 0 {
		fmt.Printf("\n── Scanning back %d candles for recommended signals ──\n", *scanBack)
		found := 0
		start := len(candles) - 1 - *scanBack
		if start < 0 {
			start = 0
		}
		for k := len(candles) - 1; k >= start; k-- {
			c := ComposeSignal(candles, a, k, weights, *threshold)
			if !c.Recommended {
				continue
			}
			found++
			fmt.Printf("  %s  %-5s  entry=%.2f  SL=%.2f  TP=%.2f  net=%+5.2f  conf=%.0f%%\n",
				candles[k].Time.Format("2006-01-02 15:04"),
				c.Side.String(), c.BestEntry, c.BestSL, c.BestTP, c.NetWeight, c.Confidence*100)
			if found >= 10 {
				fmt.Println("  …(stopping at 10)")
				break
			}
		}
		if found == 0 {
			fmt.Println("  No recommended signals in scan window.")
		}
	}
	return 0
}

func runWFA(args []string) int {
	fs := flag.NewFlagSet("wfa", flag.ContinueOnError)
	symbol := fs.String("symbol", "XAU/USD", "symbol")
	interval := fs.String("interval", "15m", "interval")
	is := fs.Int("is-months", 6, "in-sample window months")
	oos := fs.Int("oos-months", 2, "out-of-sample window months")
	step := fs.Int("step-months", 2, "rolling step in months")
	threshold := fs.Float64("threshold", 2.0, "composite |NetWeight| threshold for OOS")
	useBias := fs.Bool("bias", true, "apply H4 EMA bias filter inside each window")
	atrMult := fs.Float64("atr-mult", 1.5, "ATR SL widening multiplier (0 disables)")
	h4Period := fs.Int("h4-period", 200, "EMA period for the H4 bias")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ctx := context.Background()
	pool, err := openPool(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		return 1
	}
	defer pool.Close()

	cfg := WFAConfig{
		Symbol: *symbol, Interval: *interval,
		ISWindowMonths: *is, OOSWindowMonths: *oos, StepMonths: *step,
		Threshold: *threshold, UseBias: *useBias, ATRMult: *atrMult, H4Period: *h4Period,
	}
	res, err := RunWFA(ctx, pool, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wfa: %v\n", err)
		return 1
	}

	fmt.Printf("\n═══ Walk-Forward Analysis — %s %s ═══\n", *symbol, *interval)
	fmt.Printf("  IS=%dmo  OOS=%dmo  step=%dmo  threshold=%.2f  bias=%t  atrMult=%.2f\n\n",
		*is, *oos, *step, *threshold, *useBias, *atrMult)
	fmt.Printf("  %-3s  %-19s  %-19s  %-5s  %-7s  %-7s  %-7s\n",
		"#", "OOS Start", "OOS End", "N", "WR%", "NetR", "Sharpe")
	for i, w := range res.Windows {
		fmt.Printf("  %-3d  %-19s  %-19s  %-5d  %6.1f  %+7.2f  %+7.2f\n",
			i+1,
			w.OOSStart.Format("2006-01-02 15:04"),
			w.OOSEnd.Format("2006-01-02 15:04"),
			len(w.OOSTrades), w.OOSWinRate*100, w.OOSNetR, w.OOSSharpe)
	}

	fmt.Printf("\n── Combined OOS curve (concatenated) ────────────────────────\n")
	if res.WindowsCount == 0 || len(res.CombinedTrades) == 0 {
		fmt.Println("  No OOS trades — widen the window, lower the threshold, or check data coverage.")
		return 0
	}
	s := res.CombinedStats
	pf := fmt.Sprintf("%.2f", s.ProfitFactor)
	if math.IsInf(s.ProfitFactor, 1) {
		pf = "∞"
	}
	fmt.Printf("  Trades=%d  WR=%.1f%%  NetR=%+.2f  PF=%s  MaxDD=%.1f%%  Sharpe=%.2f  Calmar=%.2f\n",
		s.Total, s.WinRate*100, s.NetR, pf, s.MaxDDPct*100, s.Sharpe, s.Calmar)
	fmt.Printf("  Balance: $%.0f → $%.0f (%+.2f%%)\n",
		s.StartBalance, s.EndBalance, (s.EndBalance-s.StartBalance)/s.StartBalance*100)
	return 0
}

func runImportCache(args []string) int {
	fs := flag.NewFlagSet("import-cache", flag.ContinueOnError)
	file := fs.String("file", "cache_xauusd_15m.json", "path to legacy JSON cache")
	symbol := fs.String("symbol", "XAU/USD", "symbol")
	interval := fs.String("interval", "15m", "interval label")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	candles, err := loadCacheJSON(*file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load cache: %v\n", err)
		return 1
	}
	if len(candles) == 0 {
		fmt.Println("cache empty, nothing to import")
		return 0
	}

	ctx := context.Background()
	pool, err := openPool(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		return 1
	}
	defer pool.Close()

	inserted, err := upsertCandles(ctx, pool, *symbol, *interval, candles)
	if err != nil {
		fmt.Fprintf(os.Stderr, "upsert: %v\n", err)
		return 1
	}
	fmt.Printf("imported %d new candles (of %d in file) for %s %s\n",
		inserted, len(candles), *symbol, *interval)
	return 0
}
