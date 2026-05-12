package paper

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/livefeed"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/marketcal"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/meta"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/wfa"
)

// weightBundle is the immutable snapshot atomically swapped into the live
// loop on hot-reload. compose() grabs the current bundle once at the top
// of each poll, so a swap can never tear a single signal computation.
type weightBundle struct {
	flat   map[string]float64
	bySess map[string]map[string]float64
	runID  int64
}

// weightStore wraps an atomic.Pointer so compose() reads are lock-free.
// The refresh goroutine swaps in a fresh bundle when WFA finishes; the
// live loop notices on its next iteration without coordination.
type weightStore struct {
	p atomic.Pointer[weightBundle]
}

func newWeightStore(flat map[string]float64, bySess map[string]map[string]float64, runID int64) *weightStore {
	ws := &weightStore{}
	ws.p.Store(&weightBundle{flat: flat, bySess: bySess, runID: runID})
	return ws
}

func (ws *weightStore) load() *weightBundle { return ws.p.Load() }
func (ws *weightStore) swap(b *weightBundle) { ws.p.Store(b) }

// RefreshDeps is the set of dependencies the background refresh goroutine
// pulls from the caller. Kept as a struct so the CLI can wire in extra
// pieces (canonical WFA params, h4 candles when bias is on) without
// expanding RunLive's positional signature.
type RefreshDeps struct {
	// CanonicalWFA is the canonical WFA configuration used to derive new
	// weight tables. The caller fills in symbol, interval, percentile,
	// PD lookback, etc. from the live config; refresh.go layers in the
	// IS/OOS month defaults if they're zero.
	CanonicalWFA wfa.WFAConfig
	// H4Candles is loaded by the caller when CanonicalWFA.UseBias is true.
	// Nil is fine when bias is off (the user's production stance).
	H4Candles []core.Candle
}

// canonicalWFAConfigFromLive builds the WFAConfig used by the auto-refresh
// goroutine from a paper-live config. Production stance per project memory
// is bias=OFF, no London Fix, no COMEX pinning, no DXY, no triple-barrier,
// no Bayesian shrinkage, no dynamic spread — i.e. the bit-for-bit baseline
// wfa#13 setup. The user can override by passing their own RefreshDeps to
// RunLiveWithRefresh in the future.
func canonicalWFAConfigFromLive(cfg LiveConfig) wfa.WFAConfig {
	return wfa.WFAConfig{
		Symbol:              cfg.Symbol,
		Interval:            cfg.Interval,
		ISWindowMonths:      6,
		OOSWindowMonths:     1,
		StepMonths:          1,
		Threshold:           cfg.Threshold,
		ThresholdPercentile: cfg.ThresholdPercentile,
		UseBias:             false,
		ATRMult:             cfg.ATRMult,
		H4Period:            cfg.H4Period,
		PDLookback:          cfg.PDLookback,
		Slippage:            cfg.Slippage,
		SlippageNewsMult:    cfg.SlippageNewsMult,
	}
}

// shouldAutoRefresh decides whether the background WFA refresh should
// fire on this paper-live startup. Returns:
//   - fire=true if the most recent canonical run is older than one XAUUSD
//     trading day AND today is itself a trading instant
//   - the timestamp of the most recent canonical run (zero if none) for
//     the log line that explains the decision
func shouldAutoRefresh(ctx context.Context, pool *pgxpool.Pool, cfg LiveConfig, now time.Time) (bool, time.Time, error) {
	var lastRefresh *time.Time
	err := pool.QueryRow(ctx, `
		SELECT MAX(created_at) FROM backtest_runs
		WHERE symbol = $1 AND interval = $2 AND is_canonical = TRUE`,
		cfg.Symbol, cfg.Interval).Scan(&lastRefresh)
	if err != nil && err != pgx.ErrNoRows {
		return false, time.Time{}, err
	}
	if lastRefresh == nil {
		// No canonical row yet for this (symbol, interval). Bootstrap by
		// refreshing — but only if we're in a trading instant so we don't
		// burn 30-60s of compute on a market-closed Saturday.
		return marketcal.IsXAUUSDTradingDay(now), time.Time{}, nil
	}
	if marketcal.TradingDaysSince(*lastRefresh, now) < 1 {
		return false, *lastRefresh, nil
	}
	if !marketcal.IsXAUUSDTradingDay(now) {
		return false, *lastRefresh, nil
	}
	return true, *lastRefresh, nil
}

// runAutoRefresh is the goroutine body. It fetches the OHLC gap, runs a
// full WFA, persists wfa_runs/wfa_trades + a canonical mirror in
// backtest_runs/backtest_trades, loads weights from the new canonical
// row, and atomically swaps them into the live store. Errors are logged
// and swallowed — the script keeps running on the old (still valid)
// weights.
func runAutoRefresh(ctx context.Context, pool *pgxpool.Pool, cfg LiveConfig,
	currentRunID int64, store *weightStore, paperRunIDs []int64,
	deps RefreshDeps, log Logger) {

	t0 := time.Now()
	log.Log("auto-refresh: starting WFA in background (current run-id=%d)", currentRunID)

	// 1. Pull any OHLC tail that's missing. We ask for a large window
	//    (~2 trading days = 576 5m bars + padding) to cover weekend gaps
	//    after a Monday startup. TwelveData caps at 5000/req on free.
	tailBars := 600
	if fetched, err := livefeed.FetchLatest(ctx, cfg.HTTPClient, cfg.APIKey, cfg.Symbol, cfg.Interval, tailBars); err != nil {
		log.Log("auto-refresh: tail-fetch failed: %v — proceeding with DB-only history", err)
	} else if len(fetched) > 0 {
		if _, err := upsertOHLC(ctx, pool, cfg.Symbol, cfg.Interval, fetched); err != nil {
			log.Log("auto-refresh: upsert tail failed: %v — proceeding", err)
		}
	}

	// 2. Load full candle history. WFA needs at least 5000 bars; with 2y
	//    of 5m data we're well over.
	candles, err := loadAllCandles(ctx, pool, cfg.Symbol, cfg.Interval)
	if err != nil {
		log.Log("auto-refresh: load candles failed: %v — keeping current weights", err)
		return
	}

	// 3. Run WFA. This is the cost — 30-60s on 165k 5m candles.
	wfaCfg := deps.CanonicalWFA
	if wfaCfg.Symbol == "" {
		wfaCfg = canonicalWFAConfigFromLive(cfg)
	}
	res, err := wfa.RunWFA(wfaCfg, candles, deps.H4Candles)
	if err != nil {
		log.Log("auto-refresh: RunWFA failed: %v — keeping current weights", err)
		return
	}
	if len(res.CombinedTrades) == 0 {
		log.Log("auto-refresh: WFA produced zero OOS trades — keeping current weights")
		return
	}

	// 4. Persist canonical artefacts. wfa_runs gets the audit trail; the
	//    backtest_runs mirror is what LoadWeights consumes.
	wfaRunID, err := wfa.SaveWFARun(ctx, pool, wfaCfg, res)
	if err != nil {
		log.Log("auto-refresh: SaveWFARun failed: %v — keeping current weights", err)
		return
	}
	newRunID, err := persistCanonicalMirror(ctx, pool, wfaCfg, res, wfaRunID, candles)
	if err != nil {
		log.Log("auto-refresh: persist canonical mirror failed: %v — keeping current weights", err)
		return
	}

	// 5. Load the freshly-derived weights and swap. compose() will pick
	//    up the new pointer on its very next call (no signal lost).
	var newFlat map[string]float64
	var newBySess map[string]map[string]float64
	if cfg.BySession {
		newBySess, err = meta.LoadWeightsBySession(ctx, pool, newRunID, paperRunIDs...)
	} else {
		ws, lerr := meta.LoadWeights(ctx, pool, newRunID, paperRunIDs...)
		if lerr == nil {
			newFlat = meta.WeightMap(ws)
		}
		err = lerr
	}
	if err != nil {
		log.Log("auto-refresh: load new weights failed: %v — keeping current weights", err)
		return
	}

	store.swap(&weightBundle{flat: newFlat, bySess: newBySess, runID: newRunID})
	log.Log("auto-refresh: weights hot-swapped run-id=%d → run-id=%d (wfa_run=%d, %d OOS trades, %s)",
		currentRunID, newRunID, wfaRunID, len(res.CombinedTrades), time.Since(t0).Round(time.Second))
}

// persistCanonicalMirror writes the combined OOS trades from a WFA run
// into backtest_runs/backtest_trades with is_canonical=TRUE so that
// meta.LoadWeights (which queries backtest_trades) can consume them as
// if they came from a plain `backtest -persist`. The stats JSONB carries
// {source: "auto-refresh-wfa", wfa_run_id: N} so the row is auditable.
func persistCanonicalMirror(ctx context.Context, pool *pgxpool.Pool,
	cfg wfa.WFAConfig, res wfa.WFAResult, wfaRunID int64, candles []core.Candle) (int64, error) {

	statsBlob := map[string]any{
		"source":      "auto-refresh-wfa",
		"wfa_run_id":  wfaRunID,
		"windows":     res.WindowsCount,
		"oos_trades":  len(res.CombinedTrades),
		"net_r":       res.CombinedStats.NetR,
		"win_rate":    res.CombinedStats.WinRate,
		"sharpe":      res.CombinedStats.Sharpe,
		"refreshed_at": time.Now().UTC().Format(time.RFC3339),
	}
	statsJSON, err := json.Marshal(statsBlob)
	if err != nil {
		return 0, fmt.Errorf("marshal stats: %w", err)
	}
	paramsBlob := map[string]any{
		"is_months":            cfg.ISWindowMonths,
		"oos_months":           cfg.OOSWindowMonths,
		"step_months":          cfg.StepMonths,
		"threshold":            cfg.Threshold,
		"threshold_percentile": cfg.ThresholdPercentile,
		"pd_lookback":          cfg.PDLookback,
		"atr_mult":             cfg.ATRMult,
	}
	paramsJSON, err := json.Marshal(paramsBlob)
	if err != nil {
		return 0, fmt.Errorf("marshal params: %w", err)
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var newRunID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO backtest_runs (
			symbol, interval, started_at, ended_at, candle_count,
			params, stats, notes, is_canonical
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, TRUE)
		RETURNING id`,
		cfg.Symbol, cfg.Interval,
		candles[0].Time.UTC(), candles[len(candles)-1].Time.UTC(),
		len(candles),
		paramsJSON, statsJSON,
		fmt.Sprintf("auto-refresh mirror of wfa_runs.id=%d", wfaRunID),
	).Scan(&newRunID)
	if err != nil {
		return 0, fmt.Errorf("insert canonical run: %w", err)
	}

	rows := make([][]any, 0, len(res.CombinedTrades))
	for _, t := range res.CombinedTrades {
		rows = append(rows, []any{
			newRunID, t.Strategy, t.Side.String(),
			t.EntryIdx, t.ExitIdx,
			t.EntryTime.UTC(), t.ExitTime.UTC(),
			t.Entry, t.SL, t.TP, t.ExitPrice,
			t.R, t.Win, t.CandlesToExit,
			t.Session, t.YearMonth, t.YearQuarter,
			t.BalanceAfter,
		})
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"backtest_trades"},
		[]string{
			"run_id", "strategy", "side",
			"entry_idx", "exit_idx",
			"entry_time", "exit_time",
			"entry_price", "sl", "tp", "exit_price",
			"r_realized", "win", "candles_to_exit",
			"session", "year_month", "year_quarter",
			"balance_after",
		}, pgx.CopyFromRows(rows))
	if err != nil {
		return 0, fmt.Errorf("copy canonical trades: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return newRunID, nil
}
