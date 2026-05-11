package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/bias"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/capital"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/engine"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/meta"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/paper"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy/xasset"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/wfa"
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
	case "bootstrap":
		return runBootstrap(args[2:])
	case "paper-replay":
		return runPaperReplay(args[2:])
	case "paper-live":
		return runPaperLive(args[2:])
	case "capital-sim":
		return runCapitalSim(args[2:])
	case "live-signal":
		return runLiveSignal(args[2:])
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
  bootstrap     stationary block bootstrap CIs over a stored run's trades
  paper-replay  replay composite signals over historical candles with FIXED weights and persist results
  paper-live    long-running paper-trader: REST-poll latest candle every interval, signal, grade, persist
  capital-sim   simulate a fixed-risk + martingale capital scheme over a stored run's trades, per strategy
  live-signal   stream live ticks (TD WSS) and recompose the signal on REST polls
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
                         postgres://emy_admin:emy-1234@127.0.0.1:5432/candles?sslmode=disable`)
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
	pdLookback := fs.Int("pd-lookback", 0, "Premium/Discount filter lookback in candles (0 disables, 96 = 24h on 15m)")
	slippage := fs.Float64("slippage", 0, "SL slippage in dollars (0 disables)")
	slippageNewsMult := fs.Float64("slippage-news-mult", 1.0, "multiplier applied to slippage in news windows")
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

	params := engine.ParamsFor(*interval)
	if *useBias {
		h4, err := loadCandles(ctx, pool, *symbol, "4h", from, to)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load h4: %v\n", err)
			return 1
		}
		if len(h4) < *h4Period+10 {
			fmt.Fprintf(os.Stderr, "not enough H4 candles (%d) for bias period %d\n", len(h4), *h4Period)
		} else {
			params.H4Bias = bias.BuildBiasMap(h4, *h4Period, 4*time.Hour)
			fmt.Printf("Bias filter ON  (H4 EMA%d, %d bars)\n", *h4Period, len(h4))
		}
	}
	if *atrMult > 0 {
		params.ATRMult = *atrMult
		params.ATRPeriod = *atrPeriod
		fmt.Printf("ATR SL widening ON  (mult=%.2f period=%d)\n", *atrMult, *atrPeriod)
	}
	if *pdLookback > 0 {
		params.PDLookback = *pdLookback
		fmt.Printf("Premium/Discount filter ON  (lookback=%d candles)\n", *pdLookback)
	}
	if *slippage > 0 {
		params.SlippagePoints = *slippage
		params.SlippageNewsMult = *slippageNewsMult
		fmt.Printf("Slippage ON  (base=%.2f × news=%.1f)\n", *slippage, *slippageNewsMult)
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
	result := engine.RunBacktest(candles, strategy.All(), params)
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

// parseTiers parses a comma-separated list of "threshold:risk" pairs into
// a sorted []capital.Tier for the capital-sim. Pairs are ASC by threshold.
// Martingale per tier is implicitly 0 (no martingale) since the user
// configures with -risk-martingale globally; mixing tier-mart and global
// mart would be ambiguous.
func parseTiers(s string) ([]capital.Tier, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]capital.Tier, 0, len(parts))
	for _, p := range parts {
		fields := strings.Split(strings.TrimSpace(p), ":")
		if len(fields) != 2 {
			return nil, fmt.Errorf("invalid tier %q: want \"threshold:risk\"", p)
		}
		th, err := strconv.ParseFloat(strings.TrimSpace(fields[0]), 64)
		if err != nil || th < 0 {
			return nil, fmt.Errorf("invalid tier threshold %q: non-negative number required", fields[0])
		}
		r, err := strconv.ParseFloat(strings.TrimSpace(fields[1]), 64)
		if err != nil || r <= 0 || r > 1 {
			return nil, fmt.Errorf("invalid tier risk %q: 0 < r ≤ 1 required", fields[1])
		}
		out = append(out, capital.Tier{Threshold: th, FirstRiskPct: r})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Threshold < out[j].Threshold })
	return out, nil
}

// parsePaperRunIDs splits a comma-separated string like "3,7,12" into a
// []int64 of paper_run IDs. Empty input returns (nil, nil). Used by every
// subcommand that consumes weights so the paper→weights feedback loop is
// a single flag away.
func parsePaperRunIDs(s string) ([]int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		v, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("invalid paper-run entry %q: positive integer required", p)
		}
		out = append(out, v)
	}
	return out, nil
}

func runWeights(args []string) int {
	fs := flag.NewFlagSet("weights", flag.ContinueOnError)
	runID := fs.Int64("run-id", 0, "backtest_runs.id to source weights from (0 = latest)")
	paperRun := fs.String("paper-run", "", "comma-separated paper_runs.id list to UNION into the weight calc (e.g. \"2,3\")")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	paperRunIDs, err := parsePaperRunIDs(*paperRun)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
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
	ws, err := meta.LoadWeights(ctx, pool, id, paperRunIDs...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load weights: %v\n", err)
		return 1
	}
	fmt.Printf("\nWeights derived from backtest_runs.id=%d\n\n", id)
	fmt.Println(meta.FormatWeights(ws))
	return 0
}

func runSignal(args []string) int {
	fs := flag.NewFlagSet("signal", flag.ContinueOnError)
	runID := fs.Int64("run-id", 0, "backtest_runs.id to source weights (0 = latest)")
	paperRun := fs.String("paper-run", "", "comma-separated paper_runs.id list to UNION into the weight calc")
	symbol := fs.String("symbol", "XAU/USD", "symbol")
	interval := fs.String("interval", "15m", "interval to evaluate")
	threshold := fs.Float64("threshold", 2.0, "min |net weight| required to recommend")
	capital := fs.Float64("capital", 0, "USD to risk; if >0, compute Admirals units & margin")
	scanBack := fs.Int("scan-back", 0, "also scan the last N candles for any recommended signals")
	bySession := fs.Bool("by-session", false, "use per-session weights instead of the global table")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	paperRunIDs, err := parsePaperRunIDs(*paperRun)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
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
	candles, err := loadCandles(ctx, pool, *symbol, *interval, time.Time{}, time.Time{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "load candles: %v\n", err)
		return 1
	}
	if len(candles) < 200 {
		fmt.Fprintf(os.Stderr, "not enough candles (%d) — run backfill first\n", len(candles))
		return 1
	}
	a := smc.Analyze(candles)

	// Two compose modes share the rest of the runSignal loop. compose is
	// closed over either weights map so the call site stays uniform.
	var compose func(i int) meta.CompositeSignal
	if *bySession {
		ws, err := meta.LoadWeightsBySession(ctx, pool, id, paperRunIDs...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load weights-by-session: %v\n", err)
			return 1
		}
		compose = func(i int) meta.CompositeSignal {
			return meta.ComposeSignalBySession(candles, a, i, ws, *threshold)
		}
	} else {
		ws, err := meta.LoadWeights(ctx, pool, id, paperRunIDs...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load weights: %v\n", err)
			return 1
		}
		weights := meta.WeightMap(ws)
		compose = func(i int) meta.CompositeSignal {
			return meta.ComposeSignal(candles, a, i, weights, *threshold)
		}
	}

	// Always report the current candle.
	cs := compose(len(candles) - 1)
	meta.PrintCompositeSignal(*symbol, *interval, candles[len(candles)-1], cs, id, *threshold, *capital)

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
			c := compose(k)
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
	pdLookback := fs.Int("pd-lookback", 0, "Premium/Discount filter lookback in candles (0 disables)")
	slippage := fs.Float64("slippage", 0, "SL slippage in dollars (0 disables)")
	slippageNewsMult := fs.Float64("slippage-news-mult", 1.0, "multiplier applied to slippage in news windows")
	persist := fs.Bool("persist", false, "store this WFA run + every OOS trade in wfa_runs/wfa_trades")
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

	cfg := wfa.WFAConfig{
		Symbol: *symbol, Interval: *interval,
		ISWindowMonths: *is, OOSWindowMonths: *oos, StepMonths: *step,
		Threshold: *threshold, UseBias: *useBias, ATRMult: *atrMult, H4Period: *h4Period,
		PDLookback:       *pdLookback,
		Slippage:         *slippage,
		SlippageNewsMult: *slippageNewsMult,
	}
	candles, err := loadCandles(ctx, pool, cfg.Symbol, cfg.Interval, cfg.StartDate, cfg.EndDate)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load candles: %v\n", err)
		return 1
	}
	var h4Candles []core.Candle
	if cfg.UseBias {
		h4Candles, err = loadCandles(ctx, pool, cfg.Symbol, "4h", cfg.StartDate, cfg.EndDate)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load h4: %v\n", err)
			return 1
		}
	}
	res, err := wfa.RunWFA(cfg, candles, h4Candles)
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

	if *persist {
		runID, err := wfa.SaveWFARun(ctx, pool, cfg, res)
		if err != nil {
			fmt.Fprintf(os.Stderr, "persist: %v\n", err)
			return 1
		}
		fmt.Printf("\nPersisted as wfa_runs.id=%d with %d trades\n", runID, len(res.CombinedTrades))
	}
	return 0
}

func runBootstrap(args []string) int {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	runID := fs.Int64("run-id", 0, "backtest_runs.id to bootstrap (0 = latest plain run, mutually exclusive with -wfa-run)")
	wfaRunID := fs.Int64("wfa-run", 0, "wfa_runs.id to bootstrap (operates on the concatenated OOS curve)")
	iterations := fs.Int("iterations", 5000, "bootstrap iterations (≥5000 recommended for stable tails)")
	blockLen := fs.Float64("block", 40, "expected block length in trades (geometric distribution); ignored when -block-grid is set")
	blockGrid := fs.String("block-grid", "", "comma-separated block lengths (e.g. \"10,20,40,60\") — prints CI sensitivity to the geometric block parameter")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *runID != 0 && *wfaRunID != 0 {
		fmt.Fprintln(os.Stderr, "-run-id and -wfa-run are mutually exclusive")
		return 2
	}
	var grid []float64
	if *blockGrid != "" {
		for _, s := range strings.Split(*blockGrid, ",") {
			v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil || v <= 0 {
				fmt.Fprintf(os.Stderr, "invalid -block-grid entry %q: positive number required\n", s)
				return 2
			}
			grid = append(grid, v)
		}
	}

	ctx := context.Background()
	pool, err := openPool(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		return 1
	}
	defer pool.Close()

	var (
		trades []core.Trade
		header string
	)
	if *wfaRunID != 0 {
		trades, err = loadWFATrades(ctx, pool, *wfaRunID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load wfa trades: %v\n", err)
			return 1
		}
		header = fmt.Sprintf("Bootstrap — wfa_runs.id=%d (OOS curve)", *wfaRunID)
	} else {
		id := *runID
		if id == 0 {
			if err := pool.QueryRow(ctx, `SELECT id FROM backtest_runs ORDER BY id DESC LIMIT 1`).Scan(&id); err != nil {
				fmt.Fprintf(os.Stderr, "find latest run: %v\n", err)
				return 1
			}
		}
		trades, err = loadBacktestTrades(ctx, pool, id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load trades: %v\n", err)
			return 1
		}
		header = fmt.Sprintf("Bootstrap — backtest_runs.id=%d", id)
	}
	if len(trades) < 30 {
		fmt.Fprintf(os.Stderr, "only %d trades available — need at least 30 for the bootstrap to be meaningful\n", len(trades))
		return 1
	}

	startBal := engine.DefaultParams().StartBalance
	riskPct := engine.DefaultParams().RiskPct
	if len(grid) > 0 {
		fmt.Printf("\n═══ %s — block-grid sweep (%d iterations each, N=%d) ═══\n",
			header, *iterations, len(trades))
		fmt.Printf("  %-6s  %-22s  %-22s  %-22s  %-22s\n",
			"block", "Sharpe (mean, 95% CI)", "MaxDD (mean, 95% CI)", "Calmar (mean, 95% CI)", "NetR (mean, 95% CI)")
		for _, b := range grid {
			br := StationaryBlockBootstrap(trades, *iterations, b, startBal, riskPct)
			fmt.Printf("  %-6.0f  %+5.2f [%+5.2f,%+5.2f]   %4.1f%% [%4.1f%%,%4.1f%%]    %+5.2f [%+5.2f,%+5.2f]   %+6.2f [%+6.2f,%+6.2f]\n",
				b,
				br.SharpeMean, br.SharpeLow95, br.SharpeHigh95,
				br.MaxDDMean*100, br.MaxDDLow95*100, br.MaxDDHigh95*100,
				br.CalmarMean, br.CalmarLow95, br.CalmarHigh95,
				br.NetRMean, br.NetRLow95, br.NetRHigh95,
			)
		}
		return 0
	}

	br := StationaryBlockBootstrap(trades, *iterations, *blockLen, startBal, riskPct)
	fmt.Printf("\n═══ %s ═══\n", header)
	fmt.Print(formatBootstrap(br, len(trades)))
	return 0
}

func runPaperReplay(args []string) int {
	fs := flag.NewFlagSet("paper-replay", flag.ContinueOnError)
	runID := fs.Int64("run-id", 0, "backtest_runs.id whose weights drive the replay (0 = latest)")
	paperRun := fs.String("paper-run", "", "comma-separated paper_runs.id list to UNION into the weight calc (iterative paper→paper loop)")
	symbol := fs.String("symbol", "XAU/USD", "symbol")
	interval := fs.String("interval", "15m", "interval to replay")
	fromStr := fs.String("from", "", "lower bound (YYYY-MM-DD); empty = full history")
	toStr := fs.String("to", "", "upper bound (YYYY-MM-DD); empty = up to now")
	threshold := fs.Float64("threshold", 2.0, "composite |NetWeight| threshold for an entry")
	useBias := fs.Bool("bias", false, "apply H4 EMA bias filter")
	atrMult := fs.Float64("atr-mult", 0, "ATR SL widening multiplier (0 disables)")
	h4Period := fs.Int("h4-period", 200, "EMA period for the H4 bias")
	pdLookback := fs.Int("pd-lookback", 96, "Premium/Discount filter lookback in candles (0 disables, 96 = 24h on 15m)")
	slippage := fs.Float64("slippage", 0, "SL slippage in dollars (0 disables)")
	slippageNewsMult := fs.Float64("slippage-news-mult", 1.0, "multiplier applied to slippage in news windows")
	bySession := fs.Bool("by-session", false, "use per-session weight table instead of the global one")
	persist := fs.Bool("persist", false, "store this paper run + every trade in paper_runs/paper_trades")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	paperRunIDs, err := parsePaperRunIDs(*paperRun)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
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
		if err := pool.QueryRow(ctx, `SELECT id FROM backtest_runs ORDER BY id DESC LIMIT 1`).Scan(&id); err != nil {
			fmt.Fprintf(os.Stderr, "find latest backtest run: %v\n", err)
			return 1
		}
	}

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
		fmt.Fprintf(os.Stderr, "load candles: %v\n", err)
		return 1
	}
	if len(candles) < 200 {
		fmt.Fprintf(os.Stderr, "only %d candles loaded — widen the range or backfill\n", len(candles))
		return 1
	}
	var h4Candles []core.Candle
	if *useBias {
		h4Candles, err = loadCandles(ctx, pool, *symbol, "4h", from, to)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load h4: %v\n", err)
			return 1
		}
	}

	cfg := paper.Config{
		SourceRunID: id, BySession: *bySession,
		Symbol: *symbol, Interval: *interval,
		StartDate: from, EndDate: to,
		Threshold: *threshold, UseBias: *useBias, ATRMult: *atrMult, H4Period: *h4Period,
		PDLookback:       *pdLookback,
		Slippage:         *slippage,
		SlippageNewsMult: *slippageNewsMult,
	}

	var (
		flat       map[string]float64
		bySessTbl  map[string]map[string]float64
	)
	if *bySession {
		bySessTbl, err = meta.LoadWeightsBySession(ctx, pool, id, paperRunIDs...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load weights-by-session: %v\n", err)
			return 1
		}
	} else {
		ws, err := meta.LoadWeights(ctx, pool, id, paperRunIDs...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load weights: %v\n", err)
			return 1
		}
		flat = meta.WeightMap(ws)
	}

	res := paper.Replay(cfg, candles, h4Candles, flat, bySessTbl)

	fmt.Printf("\n═══ Paper Replay — %s %s ═══\n", *symbol, *interval)
	fmt.Printf("  Source: backtest_runs.id=%d  ·  bySession=%t  ·  threshold=%.2f  ·  bias=%t  ·  atrMult=%.2f\n",
		id, *bySession, *threshold, *useBias, *atrMult)
	fmt.Printf("  Range: %s → %s  (%d candles)\n\n",
		candles[0].Time.Format("2006-01-02 15:04"),
		candles[len(candles)-1].Time.Format("2006-01-02 15:04"),
		len(candles))
	s := res.Stats
	if s.Total == 0 {
		fmt.Println("  No trades — threshold too high, weights too sparse, or filters blocked everything.")
		return 0
	}
	pf := fmt.Sprintf("%.2f", s.ProfitFactor)
	if math.IsInf(s.ProfitFactor, 1) {
		pf = "∞"
	}
	fmt.Printf("  Trades=%d  WR=%.1f%%  NetR=%+.2f  PF=%s  MaxDD=%.1f%%  Sharpe=%.2f  Calmar=%.2f\n",
		s.Total, s.WinRate*100, s.NetR, pf, s.MaxDDPct*100, s.Sharpe, s.Calmar)
	fmt.Printf("  Balance: $%.0f → $%.0f (%+.2f%%)\n",
		s.StartBalance, s.EndBalance, (s.EndBalance-s.StartBalance)/s.StartBalance*100)

	// Per-strategy breakdown of who actually got credit — the attribution
	// to the WinnerStrategy lets us see which strategies are pulling weight
	// in the live regime, which is the input the weight-feedback step
	// (commit 2) will consume.
	fmt.Println("\n── Per Winner Strategy ─────────────────────────────────────")
	for _, name := range sortedKeys(s.PerStrategy) {
		ps := s.PerStrategy[name]
		fmt.Printf("  %-18s  Trades=%d  Win=%d/Loss=%d  WR=%5.1f%%  NetR=%+7.2f\n",
			name, ps.Total, ps.Wins, ps.Losses, ps.WinRate*100, ps.NetR)
	}

	if *persist {
		paperID, err := paper.SaveRun(ctx, pool, cfg,
			res, candles[0].Time, candles[len(candles)-1].Time)
		if err != nil {
			fmt.Fprintf(os.Stderr, "persist: %v\n", err)
			return 1
		}
		fmt.Printf("\nPersisted as paper_runs.id=%d with %d trades\n", paperID, len(res.Trades))
	}
	return 0
}

// stdoutLogger satisfies paper.Logger by printing to stdout with a clock
// stamp. The paper-live loop calls Log(...) for every interesting moment
// (sleep, fetch, signal, grade) so users can tail the script and see the
// system reasoning in real time. Errors are NOT printed via Log — they
// stay on stderr.
type stdoutLogger struct{}

func (stdoutLogger) Log(format string, args ...any) {
	fmt.Printf("[%s] %s\n", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
}

func runPaperLive(args []string) int {
	fs := flag.NewFlagSet("paper-live", flag.ContinueOnError)
	runID := fs.Int64("run-id", 0, "backtest_runs.id whose weights drive the live signals (0 = latest)")
	paperRun := fs.String("paper-run", "", "comma-separated paper_runs.id list to UNION into the weight calc")
	symbol := fs.String("symbol", "XAU/USD", "symbol to trade")
	interval := fs.String("interval", "15m", "candle interval (15m/1h/4h)")
	threshold := fs.Float64("threshold", 2.0, "composite |NetWeight| threshold for an entry")
	pdLookback := fs.Int("pd-lookback", 96, "Premium/Discount filter lookback in candles (0 disables, 96 = 24h on 15m)")
	slippage := fs.Float64("slippage", 0, "SL slippage in dollars (0 disables)")
	slippageNewsMult := fs.Float64("slippage-news-mult", 1.0, "multiplier applied to slippage in news windows")
	bySession := fs.Bool("by-session", false, "use per-session weight table instead of the global one")
	pollSeconds := fs.Int("poll-seconds", 0, "fixed poll cadence in seconds; 0 = align to interval boundary (recommended for free tier)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	paperRunIDs, err := parsePaperRunIDs(*paperRun)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	apiKey := os.Getenv("TWELVEDATA_API_KEY")
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "ERROR: define TWELVEDATA_API_KEY in .env (free tier is enough)")
		return 1
	}

	// SIGINT/SIGTERM → graceful shutdown: the loop finalizes the
	// paper_runs row, prints a summary, and exits 0. Without this the
	// run would still be queryable (the row was inserted at startup) but
	// ended_at would stay equal to started_at.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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
			fmt.Fprintf(os.Stderr, "find latest backtest run: %v\n", err)
			return 1
		}
	}

	// Resolve weight table ONCE at startup. To incorporate new paper
	// outcomes mid-run the user restarts the script with `-paper-run=N`
	// pointing to the just-completed run — same loop, fresher weights.
	var (
		flat      map[string]float64
		bySessTbl map[string]map[string]float64
	)
	if *bySession {
		bySessTbl, err = meta.LoadWeightsBySession(ctx, pool, id, paperRunIDs...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load weights-by-session: %v\n", err)
			return 1
		}
	} else {
		ws, err := meta.LoadWeights(ctx, pool, id, paperRunIDs...)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load weights: %v\n", err)
			return 1
		}
		flat = meta.WeightMap(ws)
	}

	cfg := paper.LiveConfig{
		SourceRunID: id, BySession: *bySession,
		Symbol: *symbol, Interval: *interval,
		Threshold:        *threshold,
		PDLookback:       *pdLookback,
		Slippage:         *slippage,
		SlippageNewsMult: *slippageNewsMult,
		PollSeconds:      *pollSeconds,
		APIKey:           apiKey,
		HTTPClient:       &http.Client{Timeout: 30 * time.Second},
	}

	fmt.Printf("═══ Paper Live — %s %s ═══\n", *symbol, *interval)
	fmt.Printf("  Source weights: backtest_runs.id=%d  ·  bySession=%t  ·  threshold=%.2f\n",
		id, *bySession, *threshold)
	if len(paperRunIDs) > 0 {
		fmt.Printf("  Feedback paper runs: %v\n", paperRunIDs)
	}
	if *pollSeconds > 0 {
		fmt.Printf("  Poll cadence: every %ds (fixed)\n", *pollSeconds)
	} else {
		fmt.Printf("  Poll cadence: aligned to %s boundary + 10s slack (~%d API calls/day)\n",
			*interval, callsPerDay(*interval))
	}
	fmt.Println("  Press Ctrl+C to stop and finalize the run.")
	fmt.Println()

	paperRunID, err := paper.RunLive(ctx, pool, cfg, flat, bySessTbl, stdoutLogger{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "live: %v\n", err)
		return 1
	}

	// Print the closing summary using the persisted paper_trades rows so
	// it reflects what's actually saved, not in-memory state.
	summarizeLiveRun(context.Background(), pool, paperRunID)
	return 0
}

// callsPerDay estimates the daily REST request count for a given interval
// at the default aligned cadence. Used only for the startup banner so the
// user knows whether their TD quota is at risk.
func callsPerDay(interval string) int {
	switch interval {
	case "5m":
		return 24 * 12 // 288 — fits under TD free-tier's 800/day
	case "15m":
		return 24 * 4 // 96
	case "1h":
		return 24
	case "4h":
		return 6
	case "1d":
		return 1
	default:
		return 0
	}
}

func summarizeLiveRun(ctx context.Context, pool *pgxpool.Pool, paperRunID int64) {
	var closed, open int
	var netR *float64
	_ = pool.QueryRow(ctx, `SELECT COUNT(*), COALESCE(SUM(r_realized), 0) FROM paper_trades WHERE paper_run_id=$1`, paperRunID).Scan(&closed, &netR)
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM paper_pending WHERE paper_run_id=$1`, paperRunID).Scan(&open)
	fmt.Println()
	fmt.Printf("── Live run paper_runs.id=%d summary ──────────────────────\n", paperRunID)
	fmt.Printf("  closed=%d  pending=%d\n", closed, open)
	if netR != nil {
		fmt.Printf("  NetR=%+.2f  (closed trades only)\n", *netR)
	}
	fmt.Printf("\n  Inspect: psql -c \"SELECT entry_time, strategy, side, r_realized, win FROM paper_trades WHERE paper_run_id=%d ORDER BY entry_time;\"\n", paperRunID)
}

func runCapitalSim(args []string) int {
	fs := flag.NewFlagSet("capital-sim", flag.ContinueOnError)
	runID := fs.Int64("run-id", 0, "backtest_runs.id whose trades drive the sim (0 = latest)")
	wfaRunID := fs.Int64("wfa-run", 0, "wfa_runs.id (use composite OOS trades instead of per-strategy backtest trades)")
	paperRunID := fs.Int64("paper-run", 0, "paper_runs.id (use graded paper trades)")
	capitalStart := fs.Float64("capital", 280, "initial bankroll in USD")
	riskFirst := fs.Float64("risk-first", 0.333, "fraction of capital risked on the first trade of the day")
	riskMart := fs.Float64("risk-martingale", 0.666, "fraction of REMAINING capital risked on the second trade after a loss")
	maxTrades := fs.Int("max-trades", 2, "max attempts per day (2 = first + one martingale)")
	reloadThresh := fs.Float64("reload-threshold", 10, "capital below this triggers a reload")
	reloadAmt := fs.Float64("reload-amount", 200, "USD the capital is set to on reload")
	minTrades := fs.Int("min-trades", 30, "skip strategies whose total trade count is below this (noise filter)")
	strategyFilter := fs.String("strategy", "", "if set, simulate ONLY trades from this strategy (works with any source flag)")
	tiersFlag := fs.String("tiers", "", "capital-band tiers \"threshold:risk[,threshold:risk]...\" — e.g. \"100000:0.1925,1000000:0.0963\" steps down to Half/Quarter Kelly at $100k/$1M")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	tiers, err := parseTiers(*tiersFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	// Mutually-exclusive source flags.
	sourceCount := 0
	if *runID != 0 {
		sourceCount++
	}
	if *wfaRunID != 0 {
		sourceCount++
	}
	if *paperRunID != 0 {
		sourceCount++
	}
	if sourceCount > 1 {
		fmt.Fprintln(os.Stderr, "-run-id, -wfa-run and -paper-run are mutually exclusive")
		return 2
	}

	ctx := context.Background()
	pool, err := openPool(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		return 1
	}
	defer pool.Close()

	cfg := capital.Config{
		StartCapital:      *capitalStart,
		FirstRiskPct:      *riskFirst,
		MartingaleRiskPct: *riskMart,
		MaxTradesPerDay:   *maxTrades,
		ReloadThreshold:   *reloadThresh,
		ReloadAmount:      *reloadAmt,
		Tiers:             tiers,
	}

	// Decide the trade source.
	var (
		header   string
		groups   map[string][]core.Trade
		oneShot  bool
		oneLabel string
	)
	switch {
	case *wfaRunID != 0:
		trades, err := loadWFATrades(ctx, pool, *wfaRunID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load wfa trades: %v\n", err)
			return 1
		}
		header = fmt.Sprintf("Capital Sim — wfa_runs.id=%d (composite OOS curve, %d trades)", *wfaRunID, len(trades))
		oneShot = true
		oneLabel = fmt.Sprintf("Composite (wfa#%d)", *wfaRunID)
		groups = map[string][]core.Trade{oneLabel: trades}
	case *paperRunID != 0:
		trades, err := loadPaperTrades(ctx, pool, *paperRunID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load paper trades: %v\n", err)
			return 1
		}
		header = fmt.Sprintf("Capital Sim — paper_runs.id=%d (live/replay trades, %d trades)", *paperRunID, len(trades))
		oneShot = true
		oneLabel = fmt.Sprintf("Paper (paper#%d)", *paperRunID)
		groups = map[string][]core.Trade{oneLabel: trades}
	default:
		id := *runID
		if id == 0 {
			if err := pool.QueryRow(ctx, `SELECT id FROM backtest_runs ORDER BY id DESC LIMIT 1`).Scan(&id); err != nil {
				fmt.Fprintf(os.Stderr, "find latest run: %v\n", err)
				return 1
			}
		}
		trades, err := loadBacktestTrades(ctx, pool, id)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load trades: %v\n", err)
			return 1
		}
		header = fmt.Sprintf("Capital Sim — backtest_runs.id=%d (per-strategy, %d trades total)", id, len(trades))
		groups = map[string][]core.Trade{}
		for _, t := range trades {
			groups[t.Strategy] = append(groups[t.Strategy], t)
		}
	}

	fmt.Printf("\n═══ %s ═══\n", header)
	fmt.Printf("  Capital=$%.2f  RiskFirst=%.1f%%  Martingale=%.1f%%  MaxTrades=%d/day  Reload@<$%.0f→$%.0f\n\n",
		cfg.StartCapital, cfg.FirstRiskPct*100, cfg.MartingaleRiskPct*100,
		cfg.MaxTradesPerDay, cfg.ReloadThreshold, cfg.ReloadAmount)

	// Optional per-strategy filter — narrows any source down to one
	// strategy's trades. Useful when you want to know what e.g. OBM
	// alone would produce on the WFA OOS curve (where it's the
	// dominant honest contributor).
	if *strategyFilter != "" {
		filtered := map[string][]core.Trade{}
		for name, ts := range groups {
			var keep []core.Trade
			for _, t := range ts {
				if t.Strategy == *strategyFilter {
					keep = append(keep, t)
				}
			}
			if len(keep) > 0 {
				filtered[name+" / "+*strategyFilter] = keep
			}
		}
		groups = filtered
	}

	// Run sim per group, collect.
	results := make([]capital.Result, 0, len(groups))
	for name, ts := range groups {
		if !oneShot && *strategyFilter == "" && len(ts) < *minTrades {
			continue
		}
		results = append(results, capital.Simulate(name, ts, cfg))
	}
	// Sort by ROI desc — winner on top.
	sort.Slice(results, func(i, j int) bool { return results[i].ROIPct > results[j].ROIPct })

	// Print comparison table.
	fmt.Printf("  %-18s  %-9s  %-9s  %-9s  %-7s  %-7s  %-6s  %-7s\n",
		"Strategy", "Final$", "Net$", "ROI%", "Days", "Mart", "Bust", "MaxDD%")
	for _, r := range results {
		fmt.Printf("  %-18s  %9.2f  %+9.2f  %+7.1f%%  %5d   %5d   %4d   %5.1f%%\n",
			r.Strategy, r.FinalCapital, r.NetProfit, r.ROIPct,
			r.DaysWithTrades, r.DaysWithMartingale, r.DaysBusted, r.MaxDrawdownPct*100)
	}

	if !oneShot {
		fmt.Println()
		fmt.Printf("  Legend: Days = days with ≥1 trade, Mart = days where martingale fired,\n")
		fmt.Printf("  Bust = days that triggered a reload, Net$ = profit MINUS total reloads paid.\n")
		fmt.Printf("  Skipped strategies with <%d trades (signal too sparse to evaluate).\n", *minTrades)
	}
	return 0
}

// loadPaperTrades pulls closed paper trades for capital-sim. Only the
// fields the sim consumes (strategy, side, r_realized, win, times) are
// hydrated — mirrors loadWFATrades.
func loadPaperTrades(ctx context.Context, pool *pgxpool.Pool, paperRunID int64) ([]core.Trade, error) {
	rows, err := pool.Query(ctx, `
		SELECT strategy, side, r_realized, win, entry_time, exit_time
		FROM paper_trades
		WHERE paper_run_id = $1
		ORDER BY entry_time ASC
	`, paperRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []core.Trade
	for rows.Next() {
		var t core.Trade
		var sideStr string
		if err := rows.Scan(&t.Strategy, &sideStr, &t.R, &t.Win, &t.EntryTime, &t.ExitTime); err != nil {
			return nil, err
		}
		if sideStr == "SHORT" {
			t.Side = core.Short
		} else {
			t.Side = core.Long
		}
		out = append(out, t)
	}
	return out, rows.Err()
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
