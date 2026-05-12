// Package paper replays the composite-signal engine over a candle slice
// using a FIXED set of strategy weights sourced from a stored backtest run
// (or per-session weight table). Each emitted trade is graded through the
// same SL/TP/time-stop lifecycle as engine.RunBacktest, then persisted to
// paper_runs/paper_trades so downstream consumers (meta.LoadWeights with
// UNION, bootstrap, manual review) can attribute outcomes to the actual
// strategy whose levels were taken.
//
// Why this exists on top of WFA: walk-forward analysis re-derives weights
// every IS→OOS step, which is the right tool to answer "would the system
// have worked on data it hadn't seen". Paper-replay holds the weights
// constant — the right tool for "if I freeze these weights today and
// commit to them for the next N months, what happens".
package paper

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/bias"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/engine"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/meta"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/wfa"
)

// Config parametrises a single paper-replay run. The weight source is
// implicit in the caller (which decides between a flat weight map and a
// per-session table) and the engine knobs mirror the backtest/WFA flags
// for parity.
type Config struct {
	SourceRunID         int64 // backtest_runs.id the weights came from
	BySession           bool  // true if weights are per-session
	Symbol              string
	Interval            string
	StartDate           time.Time // zero = first available candle
	EndDate             time.Time // zero = last available candle
	Threshold           float64
	ThresholdPercentile float64 // > 0 enables dynamic percentile gate; ignores Threshold
	UseBias             bool
	ATRMult             float64
	H4Period            int
	PDLookback          int
	Slippage            float64
	SlippageNewsMult    float64
}

// Result holds the trades produced by the replay plus aggregate stats over
// the concatenated trade stream. Stats are computed identically to
// engine.RunBacktest's so per-strategy / per-session reports stay
// interchangeable.
type Result struct {
	Trades []core.Trade
	Stats  engine.BacktestStats
}

// Replay walks `candles` with the given fixed `weights` (or `weightsBySession`
// when cfg.BySession is true), opens trades whose composite vote clears
// cfg.Threshold and survives the bias / PD / MaxSLPoints filters, then
// follows each to SL/TP/time-stop. Exactly one of `weights` or
// `weightsBySession` must be non-nil; the other should be nil.
func Replay(cfg Config, candles []core.Candle, h4Candles []core.Candle,
	weights map[string]float64, weightsBySession map[string]map[string]float64) Result {

	params := engine.ParamsFor(cfg.Interval)
	if cfg.UseBias && len(h4Candles) > 0 {
		params.H4Bias = bias.BuildBiasMap(h4Candles, cfg.H4Period, 4*time.Hour)
	}
	if cfg.ATRMult > 0 {
		params.ATRMult = cfg.ATRMult
		params.ATRPeriod = 14
	}
	params.PDLookback = cfg.PDLookback
	params.SlippagePoints = cfg.Slippage
	params.SlippageNewsMult = cfg.SlippageNewsMult

	// Build the composer once: the closure captures the chosen weight
	// surface, and wfa.SimulateOOS calls it for every candle. When the
	// dynamic-percentile gate is enabled (cfg.ThresholdPercentile > 0), a
	// fresh ThresholdBuffer is held by the closure so the running
	// distribution of |NetWeight| informs each candle's gate.
	var buf *meta.ThresholdBuffer
	if cfg.ThresholdPercentile > 0 {
		buf = meta.NewThresholdBuffer(meta.DefaultThresholdBufferCap)
	}
	var compose wfa.Composer
	switch {
	case cfg.BySession && buf != nil:
		compose = func(c []core.Candle, a smc.Analysis, i int) meta.CompositeSignal {
			return meta.ComposeSignalBySessionDynamic(c, a, i, weightsBySession, cfg.ThresholdPercentile, buf, meta.LondonFixConfig{}, meta.COMEXPinConfig{})
		}
	case cfg.BySession:
		compose = func(c []core.Candle, a smc.Analysis, i int) meta.CompositeSignal {
			return meta.ComposeSignalBySession(c, a, i, weightsBySession, cfg.Threshold, meta.LondonFixConfig{}, meta.COMEXPinConfig{})
		}
	case buf != nil:
		compose = func(c []core.Candle, a smc.Analysis, i int) meta.CompositeSignal {
			return meta.ComposeSignalDynamic(c, a, i, weights, cfg.ThresholdPercentile, buf, meta.LondonFixConfig{}, meta.COMEXPinConfig{})
		}
	default:
		compose = func(c []core.Candle, a smc.Analysis, i int) meta.CompositeSignal {
			return meta.ComposeSignal(c, a, i, weights, cfg.Threshold, meta.LondonFixConfig{}, meta.COMEXPinConfig{})
		}
	}

	trades := wfa.SimulateOOS(candles, params, compose)

	// Rebuild the running balance over the concatenated trades so the stats
	// we report match what would have happened intra-session if the trader
	// had compounded position size. Same recipe as wfa.combinedStats.
	balance := params.StartBalance
	peak := balance
	maxDD := 0.0
	for i := range trades {
		balance *= 1 + (trades[i].R * params.RiskPct)
		trades[i].BalanceAfter = balance
		if balance > peak {
			peak = balance
		}
		if peak > 0 {
			if dd := (peak - balance) / peak; dd > maxDD {
				maxDD = dd
			}
		}
	}

	return Result{
		Trades: trades,
		Stats:  engine.ComputeStats(trades, params, maxDD, balance),
	}
}

// SaveRun persists a paper-replay result atomically: one row in paper_runs
// for the header and one row per trade in paper_trades. Returns the new
// paper_runs.id. The dedicated transaction guarantees that consumers who
// load by paper_run_id never see a partial run.
func SaveRun(ctx context.Context, pool *pgxpool.Pool, cfg Config, res Result, firstCandle, lastCandle time.Time) (int64, error) {
	statsJSON, err := json.Marshal(res.Stats)
	if err != nil {
		return 0, fmt.Errorf("marshal stats: %w", err)
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var runID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO paper_runs (
			source_run_id, by_session,
			symbol, interval, started_at, ended_at,
			threshold, use_bias, atr_mult, pd_lookback,
			slippage_points, slippage_news_mult, h4_period,
			stats
		) VALUES (
			$1, $2,
			$3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13,
			$14
		) RETURNING id`,
		cfg.SourceRunID, cfg.BySession,
		cfg.Symbol, cfg.Interval, firstCandle.UTC(), lastCandle.UTC(),
		cfg.Threshold, cfg.UseBias, cfg.ATRMult, cfg.PDLookback,
		cfg.Slippage, cfg.SlippageNewsMult, cfg.H4Period,
		statsJSON,
	).Scan(&runID)
	if err != nil {
		return 0, fmt.Errorf("insert paper_run: %w", err)
	}

	if len(res.Trades) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return 0, err
		}
		return runID, nil
	}

	rows := make([][]any, 0, len(res.Trades))
	for _, t := range res.Trades {
		rows = append(rows, []any{
			runID, t.Strategy, t.Side.String(),
			t.EntryIdx, t.ExitIdx,
			t.EntryTime.UTC(), t.ExitTime.UTC(),
			t.Entry, t.SL, t.TP, t.ExitPrice,
			t.R, t.Win, t.CandlesToExit,
			t.Session, t.YearMonth, t.YearQuarter,
			t.BalanceAfter,
		})
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"paper_trades"},
		[]string{
			"paper_run_id", "strategy", "side",
			"entry_idx", "exit_idx",
			"entry_time", "exit_time",
			"entry_price", "sl", "tp", "exit_price",
			"r_realized", "win", "candles_to_exit",
			"session", "year_month", "year_quarter",
			"balance_after",
		}, pgx.CopyFromRows(rows))
	if err != nil {
		return 0, fmt.Errorf("copy paper_trades: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return runID, nil
}
