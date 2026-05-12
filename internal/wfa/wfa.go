// Package wfa runs walk-forward analyses and persists their results.
// A WFA rolls IS→OOS windows over a candle slice, derives strategy
// weights from each window's in-sample backtest, and replays the
// out-of-sample slice through meta.ComposeSignal — so the model never
// sees its own evaluation data. The OOS trades from every window are
// concatenated into the "honest curve" that downstream consumers
// (bootstrap, reporting) measure.
//
// RunWFA takes pre-loaded candle slices so this package never touches
// the OHLC table — only SaveWFARun talks to Postgres.
package wfa

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/bias"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/engine"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/indicator"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/meta"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/metrics"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// WFAConfig parametrises a single walk-forward analysis run.
//
// Time windows roll forward in StepMonths increments. Inside each window
// we (a) backtest the full strategy set on the IS slice and derive per-strategy
// weights from those results, then (b) replay the OOS slice using
// meta.ComposeSignal gated by those weights. The OOS trades from every
// window are concatenated to form the honest curve.
type WFAConfig struct {
	Symbol           string
	Interval         string
	ISWindowMonths   int       // in-sample window (weights derive from this)
	OOSWindowMonths  int       // out-of-sample window (evaluation only)
	StepMonths       int       // gap between successive IS starts
	StartDate        time.Time // zero = beginning of available data
	EndDate          time.Time // zero = up to most recent candle
	Threshold        float64   // |NetWeight| required for an OOS entry (used when ThresholdPercentile <= 0)
	// ThresholdPercentile, when > 0, switches the OOS gate from the static
	// `Threshold` to a percentile of the per-window rolling distribution of
	// |NetWeight|. The buffer is fresh per OOS window (state never bleeds
	// across windows), defaulting to DefaultThresholdBufferCap (~2000 bars).
	// Mutually exclusive with Threshold — when set, Threshold is ignored.
	ThresholdPercentile float64
	UseBias             bool
	ATRMult             float64
	H4Period            int
	PDLookback          int // Premium/Discount filter lookback in candles (0 disables)
	Slippage            float64
	SlippageNewsMult    float64
	// TripleBarrier — optional partial-TP / breakeven / dynamic-time-stop
	// overlay passed through to the engine BacktestParams used inside
	// each OOS window. Default zero-value disables the overlay.
	TripleBarrier engine.TripleBarrierConfig
	// BayesShrink toggles Bayesian shrinkage of per-strategy RR (Phase
	// 2.1). When true, RunWFA fits meta.BayesianShrinkRR on the IS
	// trade slice of each window and passes the resulting map down via
	// engine.BacktestParams.PerStrategyRR for the OOS replay — letting
	// high-volume strategies trade their own data-driven RR while
	// sparse strategies revert to the global prior (strategy.DefaultRR).
	// Default false preserves the legacy uniform-RR baseline bit-for-bit.
	BayesShrink bool
	// LondonFix toggles the 15:00 GMT London Gold Fix session adjustment
	// at vote time inside each OOS window. Zero-value (Enabled=false) is
	// a strict no-op — the legacy ComposeSignal path is preserved bit-for-bit.
	// See meta.LondonFixConfig for the microstructural rationale.
	LondonFix meta.LondonFixConfig
	// COMEXPin toggles the 48h pre-expiry pinning adjustment (Phase 3.2)
	// at vote time inside each OOS window. Zero-value (Enabled=false) is
	// a strict no-op — preserves the legacy ComposeSignal path bit-for-bit.
	// See meta.COMEXPinConfig for the microstructural rationale.
	COMEXPin meta.COMEXPinConfig
	// SpreadCurve toggles the synthetic intraday spread curve + spike
	// filter (Phase 4.1+4.2). Zero-value (Enabled=false) preserves the
	// constant SpreadDollar everywhere. See engine.SpreadCurveConfig.
	SpreadCurve engine.SpreadCurveConfig
}

// WFAWindow holds one IS→OOS slice plus the OOS-side performance.
type WFAWindow struct {
	ISStart, ISEnd   time.Time
	OOSStart, OOSEnd time.Time
	ISWeights        map[string]float64
	OOSTrades        []core.Trade
	OOSNetR          float64
	OOSWinRate       float64
	OOSSharpe        float64
}

// WFAResult is the aggregate of every window run plus the concatenated OOS curve.
type WFAResult struct {
	Windows        []WFAWindow
	CombinedTrades []core.Trade
	CombinedStats  engine.BacktestStats
	WindowsCount   int
}

// RunWFA orchestrates the walk-forward analysis end-to-end. Candles must
// already be sorted ascending by time. h4Candles can be nil when
// cfg.UseBias is false.
func RunWFA(cfg WFAConfig, candles []core.Candle, h4Candles []core.Candle) (WFAResult, error) {
	if len(candles) < 5000 {
		return WFAResult{}, fmt.Errorf("need at least 5000 candles for WFA, got %d", len(candles))
	}

	var result WFAResult
	t0 := candles[0].Time
	tEnd := candles[len(candles)-1].Time

	for cursor := t0; cursor.AddDate(0, cfg.ISWindowMonths+cfg.OOSWindowMonths, 0).Before(tEnd); cursor = cursor.AddDate(0, cfg.StepMonths, 0) {
		isStart := cursor
		isEnd := cursor.AddDate(0, cfg.ISWindowMonths, 0)
		oosStart := isEnd
		oosEnd := isEnd.AddDate(0, cfg.OOSWindowMonths, 0)
		if oosEnd.After(tEnd) {
			oosEnd = tEnd
		}

		isCandles := sliceByTime(candles, isStart, isEnd)
		if len(isCandles) < 500 {
			continue
		}
		oosCandles := sliceByTime(candles, oosStart, oosEnd)
		if len(oosCandles) < 100 {
			continue
		}

		params := engine.ParamsFor(cfg.Interval)
		if cfg.UseBias && len(h4Candles) > 0 {
			h4Slice := sliceByTime(h4Candles, isStart, oosEnd)
			params.H4Bias = bias.BuildBiasMap(h4Slice, cfg.H4Period, 4*time.Hour)
		}
		if cfg.ATRMult > 0 {
			params.ATRMult = cfg.ATRMult
			params.ATRPeriod = 14
		}
		params.PDLookback = cfg.PDLookback
		params.SlippagePoints = cfg.Slippage
		params.SlippageNewsMult = cfg.SlippageNewsMult
		params.TripleBarrier = cfg.TripleBarrier
		params.SpreadCurve = cfg.SpreadCurve

		isResult := engine.RunBacktest(isCandles, strategy.All(), params)
		weightsList := computeWeights(isResult.Trades)
		weights := make(map[string]float64, len(weightsList))
		for _, w := range weightsList {
			weights[w.Strategy] = w.Weight
		}

		// Bayesian shrinkage of per-strategy RR (Phase 2.1, opt-in).
		// EMPIRICAL VERDICT 2026-05-11: shrinkage HURTS the OOS curve on
		// this system (NetR −27% vs no-shrink baseline, regardless of
		// prior choice — static 2.0 and median-RR priors both produce
		// the same regression). Root cause: winning strategies earn
		// > 2R in OOS but the shrinkage pulls their RR DOWN toward the
		// prior, leaving expectancy on the table. The mathematics is
		// sound (sample-size-weighted variance vs bias tradeoff), but
		// the premise — that sparse per-strategy RR estimates overfit —
		// is empirically false here: the OOS rewards the per-strategy
		// MLE more than the conservative prior.
		//
		// Kept as opt-in so future experimenters can try other priors
		// (per-window estimator, per-side prior, etc.) without
		// re-implementing the shrinkage math. Default false preserves
		// the wfa#13 / wfa#14 baseline bit-for-bit.
		if cfg.BayesShrink {
			params.PerStrategyRR = meta.BayesianShrinkRR(isResult.Trades, strategy.DefaultRR)
		}

		// Fresh threshold buffer per OOS window — state should NOT bleed
		// across windows because each window is supposed to be an
		// independent OOS evaluation. The static-threshold path leaves
		// buf nil so ComposeSignal's behavior is unchanged.
		var buf *meta.ThresholdBuffer
		if cfg.ThresholdPercentile > 0 {
			buf = meta.NewThresholdBuffer(meta.DefaultThresholdBufferCap)
		}
		compose := func(c []core.Candle, a smc.Analysis, idx int) meta.CompositeSignal {
			if buf != nil {
				return meta.ComposeSignalDynamic(c, a, idx, weights, cfg.ThresholdPercentile, buf, cfg.LondonFix, cfg.COMEXPin)
			}
			return meta.ComposeSignal(c, a, idx, weights, cfg.Threshold, cfg.LondonFix, cfg.COMEXPin)
		}
		oosTrades := SimulateOOS(oosCandles, params, compose)

		win := WFAWindow{
			ISStart:   isStart,
			ISEnd:     isEnd,
			OOSStart:  oosStart,
			OOSEnd:    oosEnd,
			ISWeights: weights,
			OOSTrades: oosTrades,
		}
		summarizeWFAWindow(&win)
		result.Windows = append(result.Windows, win)
		result.CombinedTrades = append(result.CombinedTrades, oosTrades...)
	}

	result.WindowsCount = len(result.Windows)
	if len(result.CombinedTrades) > 0 {
		result.CombinedStats = combinedStats(result.CombinedTrades, engine.ParamsFor(cfg.Interval))
	}
	return result, nil
}

// sliceByTime returns the half-open slice [from, to) of a chronologically-sorted
// candle slice. We binary-search both boundaries; the input must be sorted by
// Time ascending.
func sliceByTime(candles []core.Candle, from, to time.Time) []core.Candle {
	lo := sort.Search(len(candles), func(i int) bool { return !candles[i].Time.Before(from) })
	hi := sort.Search(len(candles), func(i int) bool { return !candles[i].Time.Before(to) })
	if lo >= hi {
		return nil
	}
	return candles[lo:hi]
}

// computeWeights derives StrategyWeights from an in-memory trades slice.
// Mirrors the SQL aggregation in meta.LoadWeights so WFA windows don't need
// to persist their IS run to Postgres. Uses the same meta.SharpeWeight
// helper as LoadWeights — drift between the two would silently bias WFA.
func computeWeights(trades []core.Trade) []meta.StrategyWeight {
	byStrat := map[string][]core.Trade{}
	for _, t := range trades {
		byStrat[t.Strategy] = append(byStrat[t.Strategy], t)
	}
	out := make([]meta.StrategyWeight, 0, len(byStrat))
	for name, ts := range byStrat {
		n := len(ts)
		exp := 0.0
		wins := 0
		netR := 0.0
		for _, t := range ts {
			exp += t.R
			netR += t.R
			if t.Win {
				wins++
			}
		}
		exp /= float64(n)

		// Population stddev — same convention as metrics.SharpeRatio and
		// STDDEV_POP in the SQL aggregation.
		sq := 0.0
		for _, t := range ts {
			d := t.R - exp
			sq += d * d
		}
		stddev := math.Sqrt(sq / float64(n))

		out = append(out, meta.StrategyWeight{
			Strategy:   name,
			Trades:     n,
			Expectancy: exp,
			StdDev:     stddev,
			WinRate:    float64(wins) / float64(n),
			NetR:       netR,
			Weight:     meta.SharpeWeight(exp, stddev, n),
		})
	}
	return out
}

// Composer turns a candle index into the composite vote that decides whether
// to open a trade. Callers parameterise this so the same replay machinery
// drives WFA OOS (flat weights, ComposeSignal) and paper-trading (fixed or
// per-session weights, possibly from a different run). The function MUST be
// pure with respect to the slice — SimulateOOS calls it on every bar and
// relies on identical inputs producing identical outputs.
type Composer func(candles []core.Candle, a smc.Analysis, i int) meta.CompositeSignal

// SimulateOOS replays a candle slice using a composite-signal gate. Unlike
// engine.RunBacktest (which fires the first matching strategy per candle),
// here every strategy votes through `compose` and only the weighted majority
// produces an entry. The lifecycle (SL/TP/time-stop/spread/ATR/SL guard)
// matches RunBacktest exactly so OOS realism is preserved. Trades are tagged
// with `cs.WinnerStrategy` (the strategy whose levels were taken) so weight
// aggregation downstream credits the actual contributor, not a generic
// "Composite" sink.
func SimulateOOS(candles []core.Candle, p engine.BacktestParams, compose Composer) []core.Trade {
	a := smc.Analyze(candles)
	balance := p.StartBalance
	dayStart := balance
	currentDay := -1
	var trades []core.Trade
	var open *core.Trade
	cooldown := 0

	for i := p.MinIndex; i < len(candles); i++ {
		c := candles[i]
		ny := core.ToNY(c.Time)
		yd := ny.YearDay()
		if yd != currentDay {
			currentDay = yd
			dayStart = balance
		}
		dailyHalt := dayStart > 0 && (dayStart-balance)/dayStart >= p.DailyDDLimit

		if open != nil {
			if engine.AdvanceTrade(open, c, i, p) {
				risk := balance * p.RiskPct
				balance += risk * open.R
				open.BalanceAfter = balance
				open.Session = core.ClassifySession(open.EntryTime)
				open.YearMonth = core.YearMonthBucket(open.EntryTime)
				open.YearQuarter = core.YearQuarterBucket(open.EntryTime)
				open.CandlesToExit = open.ExitIdx - open.EntryIdx
				trades = append(trades, *open)
				open = nil
				cooldown = p.CooldownCandles
			}
			continue
		}

		if cooldown > 0 {
			cooldown--
			continue
		}
		if dailyHalt {
			continue
		}

		cs := compose(candles, a, i)
		if !cs.Recommended {
			continue
		}
		// Bias filter — match RunBacktest's contract: 0 = unknown/neutral passes.
		if b := p.H4Bias.Lookup(c.Time); b != 0 {
			if b > 0 && cs.Side == core.Short {
				continue
			}
			if b < 0 && cs.Side == core.Long {
				continue
			}
		}

		// Premium/Discount filter on the composite entry — exempt
		// breakout strategies (their entries are AT the range extreme
		// by design; the filter would always reject them). The check
		// uses cs.WinnerStrategy because that's whose levels the
		// composite chose to take.
		if p.PDLookback > 0 && !strategy.IsBreakout(cs.WinnerStrategy) {
			dr := smc.CurrentDealingRange(candles, i, p.PDLookback)
			if cs.Side == core.Long && dr.InPremium(cs.BestEntry) {
				continue
			}
			if cs.Side == core.Short && dr.InDiscount(cs.BestEntry) {
				continue
			}
		}

		entry, sl, tp := cs.BestEntry, cs.BestSL, cs.BestTP
		if p.ATRMult > 0 && p.ATRPeriod > 0 {
			atr := indicator.ATRAt(candles, i, p.ATRPeriod)
			if atr > 0 {
				mult := p.ATRMult * engine.SessionATRScale(c.Time)
				structuralD := math.Abs(entry - sl)
				atrD := atr * mult
				if atrD > structuralD {
					if cs.Side == core.Long {
						sl = entry - atrD
						tp = entry + atrD*strategy.DefaultRR
					} else {
						sl = entry + atrD
						tp = entry - atrD*strategy.DefaultRR
					}
				}
			}
		}
		// Per-strategy RR override (Bayesian shrinkage, Phase 2.1).
		// Mirrors the engine.RunBacktest path so OOS replay applies the
		// same TP transform as the IS backtest would have, keyed off the
		// strategy whose levels the composite chose to take.
		if rr, ok := p.PerStrategyRR[cs.WinnerStrategy]; ok && rr > 0 {
			tp = strategy.TPFromRR(entry, sl, rr, cs.Side == core.Long)
		}
		if p.MaxSLPoints > 0 && math.Abs(entry-sl) > p.MaxSLPoints {
			continue
		}
		if cs.Side == core.Long {
			entry += p.SpreadDollar
		} else {
			entry -= p.SpreadDollar
		}
		// Attribute the trade to the winning strategy whose levels were taken,
		// not to a generic "Composite" sink. Falls back to "Composite" only
		// in the defensive case where the composer somehow recommends without
		// nominating a winner (shouldn't happen given current ComposeSignal).
		strat := cs.WinnerStrategy
		if strat == "" {
			strat = "Composite"
		}
		open = &core.Trade{
			Strategy:  strat,
			Side:      cs.Side,
			EntryIdx:  i,
			EntryTime: c.Time,
			Entry:     entry,
			SL:        sl,
			TP:        tp,
		}
	}

	// End-of-window mark-to-market — same contract as engine.RunBacktest.
	// Without MaxHoldCandles, a trade opened late in the OOS window can
	// reach the end of the slice still open. Force-close at the last
	// bar's close so its R contributes to the window's stats instead of
	// vanishing.
	if open != nil && len(candles) > 0 {
		last := candles[len(candles)-1]
		open.ExitPrice = last.Close
		if open.Side == core.Long {
			rd := open.Entry - open.SL
			if rd > 0 {
				open.R = (last.Close - open.Entry) / rd
			}
		} else {
			rd := open.SL - open.Entry
			if rd > 0 {
				open.R = (open.Entry - last.Close) / rd
			}
		}
		open.Win = open.R > 0
		open.ExitIdx = len(candles) - 1
		open.ExitTime = last.Time
		risk := balance * p.RiskPct
		balance += risk * open.R
		open.BalanceAfter = balance
		open.Session = core.ClassifySession(open.EntryTime)
		open.YearMonth = core.YearMonthBucket(open.EntryTime)
		open.YearQuarter = core.YearQuarterBucket(open.EntryTime)
		open.CandlesToExit = open.ExitIdx - open.EntryIdx
		trades = append(trades, *open)
	}
	return trades
}

func summarizeWFAWindow(w *WFAWindow) {
	wins := 0
	for _, t := range w.OOSTrades {
		if t.Win {
			wins++
		}
		w.OOSNetR += t.R
	}
	if len(w.OOSTrades) > 0 {
		w.OOSWinRate = float64(wins) / float64(len(w.OOSTrades))
	}
	if len(w.OOSTrades) >= 2 {
		rs := make([]float64, len(w.OOSTrades))
		for i, t := range w.OOSTrades {
			rs[i] = t.R
		}
		// Annualise using this window's own trade density so windows of unequal
		// length compare fairly. Fall back to 252 (a daily-trader default) only
		// if the trades degenerate to zero elapsed time.
		span := w.OOSTrades[len(w.OOSTrades)-1].ExitTime.Sub(w.OOSTrades[0].EntryTime).Hours() / 24
		tradesPerYear := 252.0
		if span >= 1 {
			tradesPerYear = float64(len(w.OOSTrades)) * 365.0 / span
		}
		w.OOSSharpe = metrics.SharpeRatio(rs, tradesPerYear)
	}
}

// combinedStats rebuilds the running balance over the concatenated OOS trades
// and feeds the result through engine.ComputeStats so every published metric
// (Sharpe, Calmar, MaxDD, AnnRet…) is computed against the unified curve,
// not stitched together from per-window stats.
func combinedStats(trades []core.Trade, p engine.BacktestParams) engine.BacktestStats {
	balance := p.StartBalance
	peak := balance
	maxDD := 0.0
	for i := range trades {
		balance *= 1 + (trades[i].R * p.RiskPct)
		trades[i].BalanceAfter = balance
		if balance > peak {
			peak = balance
		}
		if peak > 0 {
			dd := (peak - balance) / peak
			if dd > maxDD {
				maxDD = dd
			}
		}
	}
	return engine.ComputeStats(trades, p, maxDD, balance)
}

// SaveWFARun persists a WFA result to wfa_runs + wfa_trades atomically.
// Returns the new wfa_runs.id. Every OOS trade across every window is
// stored with its 1-based window_idx so downstream consumers (bootstrap,
// reports) can keep per-window analysis even after the in-memory result
// is gone.
//
// Why a dedicated table instead of reusing backtest_runs: a backtest run
// is one IS or one plain run; a WFA run is a sequence of IS→OOS pairs
// whose only externally meaningful curve is the concatenated OOS stream.
// Forcing it into backtest_runs would either lose the windowing or pollute
// the table with rows that callers can't tell apart.
func SaveWFARun(ctx context.Context, pool *pgxpool.Pool, cfg WFAConfig, res WFAResult) (int64, error) {
	if len(res.Windows) == 0 {
		return 0, fmt.Errorf("no WFA windows to persist")
	}
	statsJSON, err := json.Marshal(res.CombinedStats)
	if err != nil {
		return 0, fmt.Errorf("marshal stats: %w", err)
	}
	startedAt := res.Windows[0].ISStart
	endedAt := res.Windows[len(res.Windows)-1].OOSEnd

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var runID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO wfa_runs (
			symbol, interval, started_at, ended_at,
			is_months, oos_months, step_months,
			threshold, use_bias, atr_mult, pd_lookback,
			slippage_points, slippage_news_mult, h4_period,
			windows_count, stats
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7,
			$8, $9, $10, $11,
			$12, $13, $14,
			$15, $16
		) RETURNING id`,
		cfg.Symbol, cfg.Interval, startedAt.UTC(), endedAt.UTC(),
		cfg.ISWindowMonths, cfg.OOSWindowMonths, cfg.StepMonths,
		cfg.Threshold, cfg.UseBias, cfg.ATRMult, cfg.PDLookback,
		cfg.Slippage, cfg.SlippageNewsMult, cfg.H4Period,
		res.WindowsCount, statsJSON,
	).Scan(&runID)
	if err != nil {
		return 0, fmt.Errorf("insert wfa_run: %w", err)
	}

	totalTrades := 0
	for _, w := range res.Windows {
		totalTrades += len(w.OOSTrades)
	}
	if totalTrades == 0 {
		if err := tx.Commit(ctx); err != nil {
			return 0, err
		}
		return runID, nil
	}

	rows := make([][]any, 0, totalTrades)
	for wi, w := range res.Windows {
		for _, t := range w.OOSTrades {
			rows = append(rows, []any{
				runID, wi + 1, t.Strategy, t.Side.String(),
				t.EntryIdx, t.ExitIdx,
				t.EntryTime.UTC(), t.ExitTime.UTC(),
				t.Entry, t.SL, t.TP, t.ExitPrice,
				t.R, t.Win, t.CandlesToExit,
				t.Session, t.YearMonth, t.YearQuarter,
				t.BalanceAfter,
			})
		}
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"wfa_trades"},
		[]string{
			"wfa_run_id", "window_idx", "strategy", "side",
			"entry_idx", "exit_idx",
			"entry_time", "exit_time",
			"entry_price", "sl", "tp", "exit_price",
			"r_realized", "win", "candles_to_exit",
			"session", "year_month", "year_quarter",
			"balance_after",
		}, pgx.CopyFromRows(rows))
	if err != nil {
		return 0, fmt.Errorf("copy wfa_trades: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return runID, nil
}
