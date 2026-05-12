package paper

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/engine"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/livefeed"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/meta"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// LiveConfig parametrises a paper-live run. PollSeconds=0 means "align to
// the next interval boundary" (the natural cadence for closed-candle
// trading and the safest one for free-tier API quotas).
type LiveConfig struct {
	SourceRunID         int64
	BySession           bool
	Symbol              string
	Interval            string
	Threshold           float64
	ThresholdPercentile float64 // > 0 enables dynamic percentile gate; ignores Threshold
	UseBias             bool
	ATRMult             float64
	H4Period            int
	PDLookback          int
	Slippage            float64
	SlippageNewsMult    float64
	PollSeconds         int
	APIKey              string
	HTTPClient          *http.Client
	// LondonFix toggles the 15:00 GMT London Gold Fix session adjustment
	// at vote time. Zero-value (Enabled=false) is a no-op — every legacy
	// paper-live run stays bit-for-bit identical until London Fix is opted in.
	LondonFix meta.LondonFixConfig
	// COMEXPin toggles the 48h COMEX pre-expiry pinning adjustment (Phase
	// 3.2) at vote time. Zero-value (Enabled=false) is a no-op — every
	// legacy paper-live run stays bit-for-bit identical until COMEX is
	// opted in. See meta.COMEXPinConfig for the microstructural rationale.
	COMEXPin meta.COMEXPinConfig
}

// pendingPosition mirrors a paper_pending row for in-memory grading. We
// reload these from DB on every iteration so a script restart picks up
// where it left off without losing state.
type pendingPosition struct {
	ID        int64
	Strategy  string
	Side      core.Side
	EntryTime time.Time
	Entry     float64
	SL        float64
	TP        float64
}

// Logger is the minimal contract paper-live needs for human output. CLI
// passes os.Stdout-backed writer; tests can capture into a buffer.
type Logger interface {
	Log(format string, args ...any)
}

// RunLive runs the paper-trading loop until ctx is cancelled. Returns the
// paper_runs.id assigned on startup so the caller can reference the row in
// further queries / weight-feedback runs. The loop is intentionally simple
// (no goroutines beyond ctx propagation): in each tick we fetch, upsert,
// signal, and grade, then sleep until the next interval boundary.
func RunLive(ctx context.Context, pool *pgxpool.Pool, cfg LiveConfig,
	weights map[string]float64, weightsBySession map[string]map[string]float64,
	log Logger) (int64, error) {

	params := buildLiveParams(cfg)

	// Persist a paper_runs row up-front. ended_at gets bumped to "now" on
	// graceful shutdown; if the script crashes, ended_at stays at the
	// startup time and the run is still queryable.
	now := time.Now().UTC()
	paperRunID, err := createLiveRun(ctx, pool, cfg, now)
	if err != nil {
		return 0, fmt.Errorf("create live run: %w", err)
	}
	gateLabel := fmt.Sprintf("threshold=%.2f", cfg.Threshold)
	if cfg.ThresholdPercentile > 0 {
		gateLabel = fmt.Sprintf("threshold-percentile=%.2f (dynamic)", cfg.ThresholdPercentile)
	}
	log.Log("paper_runs.id=%d created (live mode, source backtest_runs.id=%d, bySession=%t, %s)",
		paperRunID, cfg.SourceRunID, cfg.BySession, gateLabel)

	// Compose function is closed over the weight table chosen at startup.
	// Re-evaluating weights every poll is overkill — the underlying
	// LoadWeights call hits Postgres; we'd just be re-deriving the same
	// numbers. If the user wants to incorporate accumulating paper trades
	// they restart the script (or we add a periodic re-derive later).
	compose := buildComposer(cfg, weights, weightsBySession)

	intervalDur := intervalDuration(cfg.Interval)
	if intervalDur <= 0 {
		return paperRunID, fmt.Errorf("unsupported interval %q", cfg.Interval)
	}

	// Track the last bar-time we issued a signal on, so we don't re-signal
	// the same bar on a redundant poll. Restart-safe: read from DB so a
	// crashed script resumes cleanly.
	lastSignalled, err := loadLastSignalledTime(ctx, pool, paperRunID)
	if err != nil {
		log.Log("warn: lastSignalled lookup failed: %v", err)
	}

	for {
		select {
		case <-ctx.Done():
			return paperRunID, finalizeLiveRun(context.Background(), pool, paperRunID, time.Now().UTC(), log)
		default:
		}

		// Sleep until the next poll moment. PollSeconds=0 → align to the
		// next interval boundary + 10s slack so TD has time to publish
		// the just-closed candle. PollSeconds>0 → fixed cadence (useful
		// for paid-tier users who want lower latency).
		wait := nextPollDelay(time.Now(), intervalDur, cfg.PollSeconds)
		log.Log("sleep %s", wait.Round(time.Second))
		select {
		case <-ctx.Done():
			return paperRunID, finalizeLiveRun(context.Background(), pool, paperRunID, time.Now().UTC(), log)
		case <-time.After(wait):
		}

		// ── Fetch ───────────────────────────────────────────────────────
		// Request a small window (5 candles) so we recover gracefully
		// from a brief outage without re-running backfill.
		fetched, err := livefeed.FetchLatest(ctx, cfg.HTTPClient, cfg.APIKey, cfg.Symbol, cfg.Interval, 5)
		if err != nil {
			log.Log("fetch error: %v — will retry next poll", err)
			continue
		}
		if len(fetched) == 0 {
			log.Log("poll: 0 candles returned, skipping")
			continue
		}
		if _, err := upsertOHLC(ctx, pool, cfg.Symbol, cfg.Interval, fetched); err != nil {
			log.Log("upsert error: %v — will retry next poll", err)
			continue
		}

		// ── Load full history & pick the latest CLOSED candle ──────────
		// SMC primitives need lookback (default MinIndex=50); we load
		// everything and let the engine slice. Cheap on Postgres for
		// 50k rows.
		candles, err := loadAllCandles(ctx, pool, cfg.Symbol, cfg.Interval)
		if err != nil {
			log.Log("load candles error: %v", err)
			continue
		}
		if len(candles) < params.MinIndex+1 {
			log.Log("only %d candles available (need %d) — run `backfill` first", len(candles), params.MinIndex+1)
			continue
		}

		latestIdx := closedBarIndex(candles, intervalDur, time.Now().UTC())
		if latestIdx < 0 {
			log.Log("no closed bar available yet")
			continue
		}
		latestClosed := candles[latestIdx]

		newBar := latestClosed.Time.After(lastSignalled)
		if newBar {
			log.Log("BAR  %s UTC  close=$%.2f", latestClosed.Time.Format("2006-01-02 15:04"), latestClosed.Close)
		}

		// ── Signal on the latest closed bar (if new) ───────────────────
		if newBar {
			a := smc.Analyze(candles)
			cs := compose(candles, a, latestIdx)
			if cs.Recommended {
				p, ok, reason := acceptSignal(candles, latestIdx, cs, params)
				if ok {
					pendID, err := insertPending(ctx, pool, paperRunID, p)
					if err != nil {
						log.Log("insert pending error: %v", err)
					} else {
						log.Log("ENTRY #%d  %s  %s  entry=%.2f  SL=%.2f  TP=%.2f  |  net=%+.2f  vote=%.2f  (%s)",
							pendID, cs.Side.String(), p.Strategy, p.Entry, p.SL, p.TP,
							cs.NetWeight, cs.AppliedThreshold, cs.WinnerStrategy)
					}
				} else {
					log.Log("REJECT %s by %s — %s  |  net=%+.2f  vote=%.2f",
						cs.Side.String(), cs.WinnerStrategy, reason, cs.NetWeight, cs.AppliedThreshold)
				}
			} else {
				deficit := cs.AppliedThreshold - math.Abs(cs.NetWeight)
				log.Log("NO TRADE  long=%.2f  short=%.2f  net=%+.2f  vote=%.2f  deficit=%.2f",
					cs.LongWeight, cs.ShortWeight, cs.NetWeight, cs.AppliedThreshold, deficit)
			}
			lastSignalled = latestClosed.Time
		}

		// ── Grade pending positions ────────────────────────────────────
		pending, err := loadPending(ctx, pool, paperRunID)
		if err != nil {
			log.Log("load pending error: %v", err)
			continue
		}
		stillOpen := len(pending)
		for _, pos := range pending {
			tr, resolved := gradeOne(pos, candles, params)
			if !resolved {
				continue
			}
			if err := closePending(ctx, pool, paperRunID, pos.ID, *tr); err != nil {
				log.Log("close pending #%d error: %v", pos.ID, err)
				continue
			}
			stillOpen--
			result := "TP"
			if !tr.Win {
				result = "SL"
			}
			if params.MaxHoldCandles > 0 && tr.ExitIdx-tr.EntryIdx >= params.MaxHoldCandles {
				result = "TIMESTOP"
			}
			log.Log("RESOLVED #%d  %s  exit=$%.2f  R=%+.2f  held=%dc  (%s)",
				pos.ID, result, tr.ExitPrice, tr.R, tr.ExitIdx-tr.EntryIdx, pos.Strategy)
		}
		if stillOpen > 0 {
			log.Log("pending=%d", stillOpen)
		}
	}
}

// buildLiveParams mirrors the engine.ParamsFor + overlay logic used by
// runWFA / paper-replay so the live engine sees identical params.
func buildLiveParams(cfg LiveConfig) engine.BacktestParams {
	p := engine.ParamsFor(cfg.Interval)
	if cfg.ATRMult > 0 {
		p.ATRMult = cfg.ATRMult
		p.ATRPeriod = 14
	}
	p.PDLookback = cfg.PDLookback
	p.SlippagePoints = cfg.Slippage
	p.SlippageNewsMult = cfg.SlippageNewsMult
	return p
}

func buildComposer(cfg LiveConfig, weights map[string]float64, weightsBySession map[string]map[string]float64) func([]core.Candle, smc.Analysis, int) meta.CompositeSignal {
	// Dynamic-percentile mode: keep a buffer alive across iterations so the
	// rolling |NetWeight| distribution carries over poll-to-poll. The
	// closure capture is the only reason a sessions-long buffer survives —
	// callers must not reuse a closure across runs.
	var buf *meta.ThresholdBuffer
	if cfg.ThresholdPercentile > 0 {
		buf = meta.NewThresholdBuffer(meta.DefaultThresholdBufferCap)
	}
	switch {
	case cfg.BySession && buf != nil:
		return func(c []core.Candle, a smc.Analysis, i int) meta.CompositeSignal {
			return meta.ComposeSignalBySessionDynamic(c, a, i, weightsBySession, cfg.ThresholdPercentile, buf, cfg.LondonFix, cfg.COMEXPin)
		}
	case cfg.BySession:
		return func(c []core.Candle, a smc.Analysis, i int) meta.CompositeSignal {
			return meta.ComposeSignalBySession(c, a, i, weightsBySession, cfg.Threshold, cfg.LondonFix, cfg.COMEXPin)
		}
	case buf != nil:
		return func(c []core.Candle, a smc.Analysis, i int) meta.CompositeSignal {
			return meta.ComposeSignalDynamic(c, a, i, weights, cfg.ThresholdPercentile, buf, cfg.LondonFix, cfg.COMEXPin)
		}
	default:
		return func(c []core.Candle, a smc.Analysis, i int) meta.CompositeSignal {
			return meta.ComposeSignal(c, a, i, weights, cfg.Threshold, cfg.LondonFix, cfg.COMEXPin)
		}
	}
}

// nextPollDelay returns the time to sleep before the next fetch. When
// pollSeconds > 0 it's a fixed cadence; otherwise we align to the next
// interval boundary plus 10 seconds of slack so the just-closed bar has
// time to land in TD's API.
func nextPollDelay(now time.Time, intervalDur time.Duration, pollSeconds int) time.Duration {
	if pollSeconds > 0 {
		return time.Duration(pollSeconds) * time.Second
	}
	intervalNs := intervalDur.Nanoseconds()
	nowNs := now.UnixNano()
	// Next multiple of intervalDur boundary, in UTC seconds. Add 10s slack.
	nextBoundary := ((nowNs / intervalNs) + 1) * intervalNs
	d := time.Duration(nextBoundary-nowNs)*time.Nanosecond + 10*time.Second
	if d < time.Second {
		d = time.Second
	}
	return d
}

// closedBarIndex returns the highest index of `candles` whose bar+intervalDur
// is in the past (i.e., fully closed). Returns -1 if none qualify.
func closedBarIndex(candles []core.Candle, intervalDur time.Duration, now time.Time) int {
	for i := len(candles) - 1; i >= 0; i-- {
		if candles[i].Time.Add(intervalDur).Before(now) || candles[i].Time.Add(intervalDur).Equal(now) {
			return i
		}
	}
	return -1
}

func intervalDuration(interval string) time.Duration {
	switch interval {
	case "5m":
		return 5 * time.Minute
	case "15m":
		return 15 * time.Minute
	case "1h":
		return time.Hour
	case "4h":
		return 4 * time.Hour
	case "1d":
		return 24 * time.Hour
	default:
		return 0
	}
}

// acceptSignal applies the bias / PD / MaxSL filters that the engine and
// WFA also apply, then returns the would-be pending position with spread
// baked into entry. Returns ok=false if any gate rejects, plus a human
// reason string so the live logger can tell the operator exactly which
// filter killed the trade (and at what numbers).
func acceptSignal(candles []core.Candle, i int, cs meta.CompositeSignal, p engine.BacktestParams) (pendingPosition, bool, string) {
	c := candles[i]
	// Bias filter (only when H4Bias is set, which paper-live currently
	// doesn't enable — placeholder kept for symmetry with engine).
	if b := p.H4Bias.Lookup(c.Time); b != 0 {
		if b > 0 && cs.Side == core.Short {
			return pendingPosition{}, false, "H4 bias is BULL but composite voted SHORT"
		}
		if b < 0 && cs.Side == core.Long {
			return pendingPosition{}, false, "H4 bias is BEAR but composite voted LONG"
		}
	}
	// PD filter on the composite entry — exempt breakout strategies
	// (whose entries are AT range extremes by design, see strategy.IsBreakout).
	if p.PDLookback > 0 && !strategy.IsBreakout(cs.WinnerStrategy) {
		dr := smc.CurrentDealingRange(candles, i, p.PDLookback)
		if cs.Side == core.Long && dr.InPremium(cs.BestEntry) {
			return pendingPosition{}, false, fmt.Sprintf("PD: LONG entry $%.2f is in PREMIUM of %dc range [$%.2f-$%.2f, mid $%.2f]",
				cs.BestEntry, p.PDLookback, dr.Low, dr.High, dr.Mid)
		}
		if cs.Side == core.Short && dr.InDiscount(cs.BestEntry) {
			return pendingPosition{}, false, fmt.Sprintf("PD: SHORT entry $%.2f is in DISCOUNT of %dc range [$%.2f-$%.2f, mid $%.2f]",
				cs.BestEntry, p.PDLookback, dr.Low, dr.High, dr.Mid)
		}
	}
	// Structural levels straight from the composite — ATR widening is
	// intentionally not applied here (paper-live treats -atr-mult as a
	// no-op for v1; the structural SL is what the strategy chose). If
	// you want ATR widening in live, run paper-replay against the same
	// candles with -atr-mult set instead.
	entry, sl, tp := cs.BestEntry, cs.BestSL, cs.BestTP
	// MaxSL guard.
	if p.MaxSLPoints > 0 && math.Abs(entry-sl) > p.MaxSLPoints {
		return pendingPosition{}, false, fmt.Sprintf("MaxSL: SL distance |$%.2f - $%.2f|=$%.2f exceeds cap $%.2f",
			entry, sl, math.Abs(entry-sl), p.MaxSLPoints)
	}
	// Spread bake-in: long pays the spread on entry, short receives it.
	if cs.Side == core.Long {
		entry += p.SpreadDollar
	} else {
		entry -= p.SpreadDollar
	}

	strat := cs.WinnerStrategy
	if strat == "" {
		strat = "Composite"
	}
	return pendingPosition{
		Strategy:  strat,
		Side:      cs.Side,
		EntryTime: c.Time,
		Entry:     entry,
		SL:        sl,
		TP:        tp,
	}, true, ""
}

// gradeOne walks the candles AFTER pos.EntryTime looking for the first
// SL / TP / partial-TP / time-stop crossing using the same lifecycle as
// engine.RunBacktest and wfa.SimulateOOS — delegates to engine.AdvanceTrade
// so the three replay sites share one source of truth.
func gradeOne(pos pendingPosition, candles []core.Candle, p engine.BacktestParams) (*core.Trade, bool) {
	entryIdx := sort.Search(len(candles), func(i int) bool {
		return !candles[i].Time.Before(pos.EntryTime)
	})
	if entryIdx >= len(candles) {
		return nil, false
	}
	open := &core.Trade{
		Strategy:  pos.Strategy,
		Side:      pos.Side,
		EntryIdx:  entryIdx,
		EntryTime: pos.EntryTime,
		Entry:     pos.Entry,
		SL:        pos.SL,
		TP:        pos.TP,
	}
	// Scan forward starting at the bar AFTER entry.
	for i := entryIdx + 1; i < len(candles); i++ {
		if engine.AdvanceTrade(open, candles[i], i, p) {
			open.CandlesToExit = open.ExitIdx - entryIdx
			open.Session = core.ClassifySession(pos.EntryTime)
			open.YearMonth = core.YearMonthBucket(pos.EntryTime)
			open.YearQuarter = core.YearQuarterBucket(pos.EntryTime)
			return open, true
		}
	}
	return nil, false
}

// ── DB helpers ──────────────────────────────────────────────────────────

func createLiveRun(ctx context.Context, pool *pgxpool.Pool, cfg LiveConfig, now time.Time) (int64, error) {
	statsJSON, _ := json.Marshal(map[string]any{"mode": "live"})
	var id int64
	err := pool.QueryRow(ctx, `
		INSERT INTO paper_runs (
			source_run_id, by_session,
			symbol, interval, started_at, ended_at,
			threshold, use_bias, atr_mult, pd_lookback,
			slippage_points, slippage_news_mult, h4_period,
			stats, is_live
		) VALUES (
			$1, $2,
			$3, $4, $5, $5,
			$6, $7, $8, $9,
			$10, $11, $12,
			$13, true
		) RETURNING id`,
		cfg.SourceRunID, cfg.BySession,
		cfg.Symbol, cfg.Interval, now,
		cfg.Threshold, cfg.UseBias, cfg.ATRMult, cfg.PDLookback,
		cfg.Slippage, cfg.SlippageNewsMult, cfg.H4Period,
		statsJSON,
	).Scan(&id)
	return id, err
}

func finalizeLiveRun(ctx context.Context, pool *pgxpool.Pool, paperRunID int64, endedAt time.Time, log Logger) error {
	_, err := pool.Exec(ctx, `UPDATE paper_runs SET ended_at = $1 WHERE id = $2`, endedAt, paperRunID)
	if err != nil {
		log.Log("finalize error: %v", err)
		return err
	}
	log.Log("paper_runs.id=%d finalized (ended_at=%s)", paperRunID, endedAt.Format(time.RFC3339))
	return nil
}

func loadLastSignalledTime(ctx context.Context, pool *pgxpool.Pool, paperRunID int64) (time.Time, error) {
	// Take the max(entry_time) across BOTH paper_pending and paper_trades
	// of this run, so a restart with stuff in flight doesn't re-signal
	// the bar that already produced a pending row.
	var t *time.Time
	err := pool.QueryRow(ctx, `
		SELECT MAX(entry_time) FROM (
			SELECT entry_time FROM paper_pending WHERE paper_run_id = $1
			UNION ALL
			SELECT entry_time FROM paper_trades  WHERE paper_run_id = $1
		) s`, paperRunID).Scan(&t)
	if err != nil || t == nil {
		return time.Time{}, err
	}
	return *t, nil
}

func upsertOHLC(ctx context.Context, pool *pgxpool.Pool, symbol, interval string, candles []core.Candle) (int, error) {
	if len(candles) == 0 {
		return 0, nil
	}
	rows := make([][]any, 0, len(candles))
	for _, c := range candles {
		rows = append(rows, []any{symbol, interval, c.Time.UTC(), c.Open, c.High, c.Low, c.Close})
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE _ohlc_stage_live (
		symbol TEXT, interval TEXT, bar_time TIMESTAMPTZ,
		open DOUBLE PRECISION, high DOUBLE PRECISION,
		low DOUBLE PRECISION, close DOUBLE PRECISION
	) ON COMMIT DROP`); err != nil {
		return 0, err
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"_ohlc_stage_live"},
		[]string{"symbol", "interval", "bar_time", "open", "high", "low", "close"},
		pgx.CopyFromRows(rows))
	if err != nil {
		return 0, err
	}
	ct, err := tx.Exec(ctx, `
		INSERT INTO ohlc (symbol, interval, bar_time, open, high, low, close)
		SELECT symbol, interval, bar_time, open, high, low, close FROM _ohlc_stage_live
		ON CONFLICT (symbol, interval, bar_time) DO NOTHING`)
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int(ct.RowsAffected()), nil
}

func loadAllCandles(ctx context.Context, pool *pgxpool.Pool, symbol, interval string) ([]core.Candle, error) {
	rows, err := pool.Query(ctx, `
		SELECT bar_time, open, high, low, close FROM ohlc
		WHERE symbol = $1 AND interval = $2
		ORDER BY bar_time ASC`, symbol, interval)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]core.Candle, 0, 4096)
	for rows.Next() {
		var c core.Candle
		if err := rows.Scan(&c.Time, &c.Open, &c.High, &c.Low, &c.Close); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func insertPending(ctx context.Context, pool *pgxpool.Pool, paperRunID int64, p pendingPosition) (int64, error) {
	var id int64
	err := pool.QueryRow(ctx, `
		INSERT INTO paper_pending (
			paper_run_id, strategy, side,
			entry_time, entry_price, sl, tp,
			session, year_month, year_quarter
		) VALUES (
			$1, $2, $3,
			$4, $5, $6, $7,
			$8, $9, $10
		) RETURNING id`,
		paperRunID, p.Strategy, p.Side.String(),
		p.EntryTime.UTC(), p.Entry, p.SL, p.TP,
		core.ClassifySession(p.EntryTime),
		core.YearMonthBucket(p.EntryTime),
		core.YearQuarterBucket(p.EntryTime),
	).Scan(&id)
	return id, err
}

func loadPending(ctx context.Context, pool *pgxpool.Pool, paperRunID int64) ([]pendingPosition, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, strategy, side, entry_time, entry_price, sl, tp
		FROM paper_pending
		WHERE paper_run_id = $1
		ORDER BY entry_time`, paperRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pendingPosition
	for rows.Next() {
		var p pendingPosition
		var sideStr string
		if err := rows.Scan(&p.ID, &p.Strategy, &sideStr, &p.EntryTime, &p.Entry, &p.SL, &p.TP); err != nil {
			return nil, err
		}
		if sideStr == "SHORT" {
			p.Side = core.Short
		} else {
			p.Side = core.Long
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// closePending moves a pending row into paper_trades and deletes it from
// paper_pending in one transaction. After this returns, meta.LoadWeights
// invoked with `-paper-run=<paperRunID>` will see the resolved trade in
// its weight calculation.
func closePending(ctx context.Context, pool *pgxpool.Pool, paperRunID, pendingID int64, t core.Trade) error {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx, `
		INSERT INTO paper_trades (
			paper_run_id, strategy, side,
			entry_idx, exit_idx, entry_time, exit_time,
			entry_price, sl, tp, exit_price,
			r_realized, win, candles_to_exit,
			session, year_month, year_quarter
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		paperRunID, t.Strategy, t.Side.String(),
		t.EntryIdx, t.ExitIdx, t.EntryTime.UTC(), t.ExitTime.UTC(),
		t.Entry, t.SL, t.TP, t.ExitPrice,
		t.R, t.Win, t.CandlesToExit,
		t.Session, t.YearMonth, t.YearQuarter,
	)
	if err != nil {
		return fmt.Errorf("insert paper_trade: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM paper_pending WHERE id = $1`, pendingID); err != nil {
		return fmt.Errorf("delete paper_pending: %w", err)
	}
	return tx.Commit(ctx)
}
