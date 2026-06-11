package paper

import (
	"bytes"
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
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/marketcal"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/meta"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/notify"
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
	// 3.2) at vote time. Zero-value (Enabled=false) is a no-op.
	COMEXPin meta.COMEXPinConfig
	// SpreadCurve toggles the synthetic intraday spread curve + spike
	// filter (Phase 4.1+4.2). Zero-value (Enabled=false) is a no-op.
	SpreadCurve engine.SpreadCurveConfig
	// DXY (Phase 3.3) is the intermarket-divergence weight adjustment.
	// Zero-value (Enabled=false) is a no-op.
	DXY meta.DXYFilterConfig
	// ForceRefresh bypasses the staleness gate in shouldAutoRefresh so the
	// auto-refresh WFA fires on startup even when the latest canonical is
	// less than one trading day old. Use after a manual backfill to rebuild
	// the canonical on freshly-completed data without waiting for the next
	// trading day.
	ForceRefresh bool
	// Notifier, when non-nil, receives one GreenAlert per ENTRY-tier signal
	// (|net| ≥ 2.0) the instant it fires in the live loop. Zero-value (nil)
	// disables notifications — every legacy run stays bit-for-bit identical.
	Notifier notify.Notifier
	// LiveFeedback, when Enabled, shrinks the canonical weights toward the
	// clean live paper-trade estimate at startup and on every auto-refresh
	// (Tema 2). Zero-value (Enabled=false) → canonical-only, bit-for-bit legacy.
	LiveFeedback meta.LiveFeedbackConfig
	// LiveExecute, when true, fires a fire-and-forget POST to the MT5 bridge
	// (ExecEndpoint) on every green ENTRY signal — in parallel to the green
	// alert and INDEPENDENT of the paper trade gate (the broker's own
	// one-at-a-time, enforced in the bridge, arbitrates real orders, not the
	// paper layer). The bridge owns sizing, dedup and the arm/dry-run gate.
	// Zero-value (false) disables it — every legacy run stays bit-for-bit
	// identical and NO orders are ever sent.
	LiveExecute  bool
	ExecEndpoint string  // MT5 bridge order endpoint, e.g. http://127.0.0.1:9099/order
	ExecRiskPct  float64 // requested risk at the SL; the bridge caps it at its own ceiling
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
	paperRunIDs []int64, log Logger) (int64, error) {

	params := buildLiveParams(cfg)

	intervalDur := intervalDuration(cfg.Interval)
	if intervalDur <= 0 {
		return 0, fmt.Errorf("unsupported interval %q", cfg.Interval)
	}

	// Atomic-pointer store so the background auto-refresh goroutine can
	// hot-swap weights without coordinating with the signal loop. compose()
	// loads the current bundle once at the top of each poll, so a swap
	// can never split a single signal computation.
	store := newWeightStore(weights, weightsBySession, cfg.SourceRunID)
	compose := buildComposer(cfg, store)

	// Spawn the auto-refresh goroutine if at least one XAUUSD trading day
	// has passed since the most recent canonical row AND today is itself
	// a trading instant. Logs the decision either way so the operator can
	// see why nothing fired on a weekend / holiday.
	startupForRefresh := time.Now().UTC()
	if fire, lastRefresh, err := shouldAutoRefresh(ctx, pool, cfg, startupForRefresh); err != nil {
		log.Log("auto-refresh: decision query failed: %v — skipping", err)
	} else if !fire {
		if lastRefresh.IsZero() {
			log.Log("auto-refresh: skipped (no canonical row yet, but today is not a trading instant)")
		} else {
			days := marketcal.TradingDaysSince(lastRefresh, startupForRefresh)
			log.Log("auto-refresh: skipped (last canonical %s ago = %d trading days, today_is_trading=%t)",
				startupForRefresh.Sub(lastRefresh).Round(time.Minute), days,
				marketcal.IsXAUUSDTradingDay(startupForRefresh))
		}
	} else {
		go runAutoRefresh(ctx, pool, cfg, cfg.SourceRunID, store, paperRunIDs, RefreshDeps{}, lastRefresh, log)
	}

	// Replay any ENTRY-tier signals (|net|≥2.0) that closed since the last
	// graceful shutdown, capped at 24h. Runs BEFORE createLiveRun so the
	// query naturally sees only prior runs. Fetch-on-startup keeps the
	// digest accurate even if `backfill` hasn't been run recently.
	startupNow := time.Now().UTC()
	if err := replayMissedSignals(ctx, pool, cfg, compose, params, intervalDur, startupNow, log); err != nil {
		log.Log("replay error: %v — continuing", err)
	}

	// Persist a paper_runs row up-front. ended_at gets bumped to "now" on
	// graceful shutdown; if the script crashes, ended_at stays at the
	// startup time and the run is still queryable.
	paperRunID, err := createLiveRun(ctx, pool, cfg, startupNow)
	if err != nil {
		return 0, fmt.Errorf("create live run: %w", err)
	}
	gateLabel := fmt.Sprintf("threshold=%.2f", cfg.Threshold)
	if cfg.ThresholdPercentile > 0 {
		gateLabel = fmt.Sprintf("threshold-percentile=%.2f (dynamic)", cfg.ThresholdPercentile)
	}
	log.Log("paper_runs.id=%d created (live mode, source backtest_runs.id=%d, bySession=%t, %s)",
		paperRunID, cfg.SourceRunID, cfg.BySession, gateLabel)

	// Track the last bar-time we issued a signal on, so we don't re-signal
	// the same bar on a redundant poll. Restart-safe: read from DB so a
	// crashed script resumes cleanly.
	lastSignalled, err := loadLastSignalledTime(ctx, pool, paperRunID)
	if err != nil {
		log.Log("warn: lastSignalled lookup failed: %v", err)
	}

	// Per-loop SMC cache. Seeded on the first iteration via Reset, then
	// Append-extended bar-by-bar — collapses the 23s-per-poll cost of
	// smc.Analyze on 165k candles into a 221µs per-bar update. wfa /
	// paper-replay keep using batch Analyze; the analyzer is scoped to
	// this loop and never escapes.
	analyzer := smc.NewIncrementalAnalyzer()

	// One-at-a-time + each-setup-once gate state. `seen` holds the structural
	// identity (Strategy+Side+Entry+SL+TP) of every setup already opened, so a
	// persistent SMC anchor that keeps recommending across bars yields ONE
	// trade, not N — count parity with the engine (which spaces re-entries via
	// CooldownCandles). Seeded from the last 24h of live trades/pendings (same
	// symbol+interval) so a service restart doesn't re-open a setup still on
	// the chart. `alerted` dedups the green WhatsApp per identity (process-
	// local; a freshly-restarted process may re-alert a persistent green once).
	seen, err := loadRecentSignalKeys(ctx, pool, cfg, startupNow.Add(-replayMaxWindow))
	if err != nil {
		log.Log("warn: seed seen-set failed: %v — starting empty", err)
		seen = make(map[string]bool)
	}
	alerted := make(map[string]bool)
	// executed mirrors `alerted` for the real-order path: at most one POST to
	// the MT5 bridge per structural identity per process. Only consulted when
	// cfg.LiveExecute is set; the broker's one-at-a-time is the hard gate.
	executed := make(map[string]bool)

	for {
		select {
		case <-ctx.Done():
			return paperRunID, finalizeLiveRun(context.Background(), pool, paperRunID, time.Now().UTC(), log)
		default:
		}

		// Gate the loop on XAUUSD market hours. During weekly close
		// (Fri 21 UTC → Sun 22 UTC) and COMEX holidays we sleep straight
		// to the reopen instead of polling uselessly: saves TwelveData
		// quota (~576 calls/weekend at 5m cadence) and keeps logs from
		// drowning in NO-TRADE noise on stale Friday-close bars.
		if now := time.Now().UTC(); !marketcal.IsXAUUSDTradingDay(now) {
			nextOpen := marketcal.NextMarketOpen(now)
			closedFor := time.Until(nextOpen) + 30*time.Second
			log.Log("market closed — sleeping %s until %s UTC reopen",
				closedFor.Round(time.Minute), nextOpen.Format("Mon 2006-01-02 15:04"))
			select {
			case <-ctx.Done():
				return paperRunID, finalizeLiveRun(context.Background(), pool, paperRunID, time.Now().UTC(), log)
			case <-time.After(closedFor):
			}
			continue
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

		// Sync the SMC cache with whatever loadAllCandles just returned.
		// Slice up to latestIdx+1 so any open bar accidentally upserted
		// from livefeed never reaches the analyzer (its OHLC mutates as
		// the bar fills, which would corrupt append-only swing/FVG state).
		// First poll seeds via Reset (≈16s on 165k bars); steady-state
		// Appends the new tail in microseconds. Any time/length mismatch
		// triggers a full rebuild rather than papering over corruption.
		syncSMCAnalyzer(analyzer, candles[:latestIdx+1], log)

		// ── Grade pending positions FIRST ──────────────────────────────
		// Resolve any open trade before considering a new entry: a trade
		// that hits SL/TP on this bar must free the single-position slot
		// immediately, with no artificial wait, so a different setup can
		// open the instant the prior one closes. This mirrors the engine,
		// which advances/closes the open trade on candle i BEFORE evaluating
		// a new signal (engine.go). openCount drives the one-at-a-time gate.
		pending, err := loadPending(ctx, pool, paperRunID)
		if err != nil {
			log.Log("load pending error: %v", err)
			continue
		}
		openCount := len(pending)
		for _, pos := range pending {
			tr, resolved := gradeOne(pos, candles, params)
			if !resolved {
				continue
			}
			if err := closePending(ctx, pool, paperRunID, pos.ID, *tr); err != nil {
				log.Log("close pending #%d error: %v", pos.ID, err)
				continue
			}
			openCount--
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
		if openCount > 0 {
			log.Log("pending=%d", openCount)
		}

		// ── Signal on the latest closed bar (if new) ───────────────────
		if newBar {
			a := analyzer.Analysis()
			cs := compose(candles, a, latestIdx)
			if cs.Recommended {
				p, ok, reason := acceptSignal(candles, latestIdx, cs, params)
				if ok {
					key := signalKey(p.Strategy, p.Side.String(), p.Entry, p.SL, p.TP)
					// Strength prefix lets the CLI logger colorize by
					// conviction: |net| ≥ 2.0 → STRONG (green), 1.0–2.0 →
					// MED (cyan), <1.0 → WEAK (yellow). Thresholds derive
					// from the production weight table (OBM 2.24,
					// SilverBullet 2.13, FVGFill 1.65, DailyBreakout 0.14).
					absNet := math.Abs(cs.NetWeight)
					label := "ENTRY-WEAK"
					switch {
					case absNet >= 2.0:
						label = "ENTRY"
					case absNet >= 1.0:
						label = "ENTRY-MED"
					}

					// Green alert (buena) — DECOUPLED from the trade decision.
					// Fire once per structural identity (Strategy+Side+Entry+
					// SL+TP) the first time it reaches ENTRY tier (|net|≥2.0),
					// independent of whether a trade is currently open or this
					// setup was already traded. Under one-at-a-time a setup that
					// opens in MED and only later strengthens to green would
					// otherwise never notify. Fire-and-forget; errors logged,
					// never fatal — same discipline as shadow recording below.
					// In-memory dedup (alerted) → no spam across a persistent
					// setup; a freshly-restarted process may re-alert once.
					if absNet >= 2.0 && cfg.Notifier != nil && !alerted[key] {
						alerted[key] = true
						alert := notify.GreenAlert{
							Time:      latestClosed.Time,
							Symbol:    cfg.Symbol,
							Interval:  cfg.Interval,
							Side:      cs.Side,
							Strategy:  p.Strategy,
							Entry:     p.Entry,
							SL:        p.SL,
							TP:        p.TP,
							NetWeight: cs.NetWeight,
							Threshold: cs.AppliedThreshold,
						}
						go func() {
							nctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
							defer cancel()
							if err := cfg.Notifier.Notify(nctx, alert); err != nil {
								log.Log("wpp notify error (%s %s): %v", alert.Side.String(), alert.Strategy, err)
							}
						}()
					}

					// Live execution — fire a REAL order at the MT5 bridge in
					// PARALLEL to the green alert, gated only by |net|≥2.0 and a
					// per-identity once (executed). Deliberately independent of
					// the paper trade gate below: a MED paper trade holding the
					// slot must not block a real green, and the broker's own
					// one-at-a-time (enforced inside the bridge) is what arbitrates
					// real orders. Fire-and-forget; errors logged, never fatal —
					// same discipline as the notify. Off unless -live-execute.
					if cfg.LiveExecute && absNet >= 2.0 && !executed[key] {
						executed[key] = true
						brokerSide := "SELL"
						if cs.Side == core.Long {
							brokerSide = "BUY"
						}
						go postOrder(cfg.ExecEndpoint, brokerSide, p.Entry, p.SL, p.TP,
							cfg.ExecRiskPct, p.Strategy, key, log)
					}

					// Trade gate — one-at-a-time + each-setup-once. The live
					// must count like the engine: at most one open position at
					// a time, and a given structural setup opens EXACTLY once.
					// "each-setup-once" replaces the engine's post-close
					// CooldownCandles and kills the duplicate-stacking bug
					// where one structural decision was recorded as N trades
					// (e.g. 7× SilverBullet SHORT 4319.80). A different setup
					// may open the instant the open trade closes — no wait.
					switch decideTradeAction(openCount, seen, key) {
					case actionHoldOpen:
						log.Log("HOLD %s %s — trade open, skipping new entry  |  net=%+.2f  vote=%.2f",
							label, p.Strategy, cs.NetWeight, cs.AppliedThreshold)
					case actionDedup:
						log.Log("DEDUP %s %s  entry=%.2f  SL=%.2f  TP=%.2f — setup already traded this run",
							label, p.Strategy, p.Entry, p.SL, p.TP)
					default: // actionInsert
						pendID, err := insertPending(ctx, pool, paperRunID, p)
						if err != nil {
							log.Log("insert pending error: %v", err)
						} else {
							seen[key] = true
							openCount++
							log.Log("%s #%d  %s  %s  entry=%.2f  SL=%.2f  TP=%.2f  |  net=%+.2f  vote=%.2f  (%s)",
								label, pendID, cs.Side.String(), p.Strategy, p.Entry, p.SL, p.TP,
								cs.NetWeight, cs.AppliedThreshold, cs.WinnerStrategy)
						}
					}
				} else {
					log.Log("REJECT %s by %s — %s  |  net=%+.2f  vote=%.2f",
						cs.Side.String(), cs.WinnerStrategy, reason, cs.NetWeight, cs.AppliedThreshold)
					// Shadow: persist a virtual position with the same
					// structural entry/SL/TP so we can audit later whether
					// the gate that just fired is earning its rejection.
					// Errors are logged but never abort the loop — shadow
					// is informational, not load-bearing.
					if shadowID, err := recordShadow(ctx, pool, paperRunID, cs, reason, latestClosed.Time); err != nil {
						log.Log("shadow record error: %v", err)
					} else if shadowID > 0 {
						log.Log("SHADOW #%d  %s  %s  entry=%.2f  SL=%.2f  TP=%.2f  (rejected by %s)",
							shadowID, cs.Side.String(), cs.WinnerStrategy, cs.BestEntry, cs.BestSL, cs.BestTP, classifyRejectGate(reason))
					}
				}
			} else {
				deficit := cs.AppliedThreshold - math.Abs(cs.NetWeight)
				log.Log("NO TRADE  long=%.2f  short=%.2f  net=%+.2f  vote=%.2f  deficit=%.2f",
					cs.LongWeight, cs.ShortWeight, cs.NetWeight, cs.AppliedThreshold, deficit)
			}
			lastSignalled = latestClosed.Time
		}

		// ── Grade open shadow positions ────────────────────────────────
		// Same lifecycle as pending: walk candles via AdvanceTrade, flip
		// to status='closed' on resolution. Shadow errors never abort the
		// loop — informational only.
		shadows, err := loadOpenShadows(ctx, pool, paperRunID)
		if err != nil {
			log.Log("load shadows error: %v", err)
			continue
		}
		shadowOpen := len(shadows)
		for _, sh := range shadows {
			tr, resolved := gradeShadow(sh, candles, params)
			if !resolved {
				continue
			}
			if err := closeShadow(ctx, pool, sh.ID, *tr); err != nil {
				log.Log("close shadow #%d error: %v", sh.ID, err)
				continue
			}
			shadowOpen--
			result := "TP"
			if !tr.Win {
				result = "SL"
			}
			if params.MaxHoldCandles > 0 && tr.ExitIdx-tr.EntryIdx >= params.MaxHoldCandles {
				result = "TIMESTOP"
			}
			log.Log("SHADOW-RESOLVED #%d  %s  exit=$%.2f  R=%+.2f  held=%dc  (%s, gate=%s)",
				sh.ID, result, tr.ExitPrice, tr.R, tr.ExitIdx-tr.EntryIdx, sh.Strategy, sh.RejectGate)
		}
		if shadowOpen > 0 {
			log.Log("shadows_open=%d", shadowOpen)
		}
	}
}

// syncSMCAnalyzer brings the per-loop IncrementalAnalyzer in line with the
// candle slice that loadAllCandles just returned. Three paths:
//
//   - Empty analyzer → first iteration, Reset to seed from full history.
//   - Tail match (analyzer.LastTime == candles[Len()-1].Time) → fast path,
//     Append only the new bars at the end.
//   - Anything else (shrunken DB, time drift, gap in the middle) → Reset
//     and log the reason so the operator can see why the cache rebuilt.
//
// Reset paths emit a log line (operators can grep "smc cache" to spot
// unexpected rebuilds); the Append fast path stays silent because it runs
// on every poll and would dominate the log otherwise.
func syncSMCAnalyzer(a *smc.IncrementalAnalyzer, candles []core.Candle, log Logger) {
	n := a.Len()
	if n == 0 {
		t := time.Now()
		a.Reset(candles)
		log.Log("smc cache seeded: %d bars in %s", len(candles), time.Since(t).Round(time.Millisecond))
		return
	}
	if n > len(candles) {
		t := time.Now()
		a.Reset(candles)
		log.Log("smc cache rebuilt: db shrank (analyzer=%d, db=%d) in %s",
			n, len(candles), time.Since(t).Round(time.Millisecond))
		return
	}
	if !candles[n-1].Time.Equal(a.LastTime()) {
		t := time.Now()
		a.Reset(candles)
		log.Log("smc cache rebuilt: time mismatch at index %d (analyzer=%s, db=%s) in %s",
			n-1, a.LastTime().Format(time.RFC3339), candles[n-1].Time.Format(time.RFC3339),
			time.Since(t).Round(time.Millisecond))
		return
	}
	if len(candles) == n {
		return // nothing new
	}
	for _, c := range candles[n:] {
		a.Append(c)
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
	p.SpreadCurve = cfg.SpreadCurve
	return p
}

func buildComposer(cfg LiveConfig, store *weightStore) func([]core.Candle, smc.Analysis, int) meta.CompositeSignal {
	// Dynamic-percentile mode: keep a buffer alive across iterations so the
	// rolling |NetWeight| distribution carries over poll-to-poll. The
	// closure capture is the only reason a sessions-long buffer survives —
	// callers must not reuse a closure across runs.
	//
	// IMPORTANT: the buffer survives across weight hot-swaps too. The
	// distribution of |NetWeight| changes shape AFTER a swap, but the
	// rolling window will naturally re-converge over ~DefaultThresholdBufferCap
	// bars. Resetting would create a cold gate for ~2000 bars after every
	// refresh — much worse than the transient distribution shift.
	var buf *meta.ThresholdBuffer
	if cfg.ThresholdPercentile > 0 {
		buf = meta.NewThresholdBuffer(meta.DefaultThresholdBufferCap)
	}
	// The DXY pointer is captured into the closure so its lazy structure
	// cache survives across polls (same rationale as buf above). Caller
	// must keep cfg alive across the loop.
	dxy := &cfg.DXY
	switch {
	case cfg.BySession && buf != nil:
		return func(c []core.Candle, a smc.Analysis, i int) meta.CompositeSignal {
			b := store.load()
			return meta.ComposeSignalBySessionDynamic(c, a, i, b.bySess, cfg.ThresholdPercentile, buf, cfg.LondonFix, cfg.COMEXPin, dxy)
		}
	case cfg.BySession:
		return func(c []core.Candle, a smc.Analysis, i int) meta.CompositeSignal {
			b := store.load()
			return meta.ComposeSignalBySession(c, a, i, b.bySess, cfg.Threshold, cfg.LondonFix, cfg.COMEXPin, dxy)
		}
	case buf != nil:
		return func(c []core.Candle, a smc.Analysis, i int) meta.CompositeSignal {
			b := store.load()
			return meta.ComposeSignalDynamic(c, a, i, b.flat, cfg.ThresholdPercentile, buf, cfg.LondonFix, cfg.COMEXPin, dxy)
		}
	default:
		return func(c []core.Candle, a smc.Analysis, i int) meta.CompositeSignal {
			b := store.load()
			return meta.ComposeSignal(c, a, i, b.flat, cfg.Threshold, cfg.LondonFix, cfg.COMEXPin, dxy)
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

// signalKey is the structural identity of a setup: two setups with the same
// (Strategy, Side, Entry, SL, TP) anchor to the same SMC level and are the
// same decision. The same anchor recomputed on a later bar yields BIT-IDENTICAL
// levels (the duplicate-stacking rows were all `4319.800985` to full precision),
// so %g (shortest round-trip) collapses true duplicates exactly while never
// merging two distinct setups that happen to sit within a cent of each other.
// double precision round-trips losslessly through Postgres, so a key built from
// a DB-seeded entry_price matches one built in-process. Side is the stored
// string ("LONG"/"SHORT") so DB-seeded and in-process (core.Side.String()) keys
// collide exactly.
func signalKey(strategy, side string, entry, sl, tp float64) string {
	return fmt.Sprintf("%s|%s|%g|%g|%g", strategy, side, entry, sl, tp)
}

// postOrder fires a fire-and-forget order intent at the MT5 bridge (Fase 3).
// The bridge owns sizing, one-at-a-time vs the broker, each-setup-once and the
// arm/dry-run gate; here we only translate a green into an HTTP POST and log the
// outcome. `side` is the broker direction (BUY/SELL); `key` is the structural
// identity used for the bridge's own dedup. Errors are never fatal — identical
// discipline to the green notify.
func postOrder(endpoint, side string, entry, sl, tp, riskPct float64, strategy, key string, log Logger) {
	body, err := json.Marshal(map[string]any{
		"side": side, "entry": entry, "sl": sl, "tp": tp,
		"strategy": strategy, "key": key, "risk_pct": riskPct,
	})
	if err != nil {
		log.Log("live-execute: marshal error: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		log.Log("live-execute: request error: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Log("live-execute: POST %s error (%s %s): %v", endpoint, side, strategy, err)
		return
	}
	defer resp.Body.Close()
	var r struct {
		OK     bool    `json:"ok"`
		DryRun bool    `json:"dry_run"`
		Lots   float64 `json:"lots"`
		Reason string  `json:"reason"`
		Ticket *int64  `json:"ticket"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		log.Log("live-execute: decode error (%s %s): %v", side, strategy, err)
		return
	}
	var tk int64
	if r.Ticket != nil {
		tk = *r.Ticket
	}
	log.Log("live-execute %s %s: ok=%v dry=%v lots=%g ticket=%d reason=%q",
		side, strategy, r.OK, r.DryRun, r.Lots, tk, r.Reason)
}

// tradeAction is the live loop's decision for an accepted signal, mirroring
// the engine's single-position discipline plus each-setup-once dedup.
type tradeAction int

const (
	actionInsert   tradeAction = iota // slot free and setup unseen → open it
	actionHoldOpen                    // a trade is already open → skip (one-at-a-time)
	actionDedup                       // this setup already opened once → skip
)

// decideTradeAction is the pure core of the trade gate, factored out so it can
// be unit-tested without a database. openCount is the number of still-open
// pendings AFTER grading this bar; seen holds identities already opened.
func decideTradeAction(openCount int, seen map[string]bool, key string) tradeAction {
	switch {
	case openCount > 0:
		return actionHoldOpen
	case seen[key]:
		return actionDedup
	default:
		return actionInsert
	}
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

// loadRecentSignalKeys seeds the each-setup-once gate from recent live history
// so a service restart doesn't re-open a setup that's still on the chart. It
// collects the structural identity of every pending/closed trade from LIVE
// runs of the same symbol+interval with entry_time >= since (24h window, same
// cap as replayMissedSignals). Beyond the window the set self-heals: an
// identical (Strategy,Side,Entry,SL,TP) recurring a full day later is treated
// as a genuinely fresh setup rather than a stale duplicate.
func loadRecentSignalKeys(ctx context.Context, pool *pgxpool.Pool, cfg LiveConfig, since time.Time) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `
		SELECT strategy, side, entry_price, sl, tp FROM (
			SELECT pp.strategy, pp.side, pp.entry_price, pp.sl, pp.tp, pp.entry_time
			FROM paper_pending pp JOIN paper_runs r ON r.id = pp.paper_run_id
			WHERE r.is_live AND r.symbol = $1 AND r.interval = $2 AND pp.entry_time >= $3
			UNION ALL
			SELECT pt.strategy, pt.side, pt.entry_price, pt.sl, pt.tp, pt.entry_time
			FROM paper_trades pt JOIN paper_runs r ON r.id = pt.paper_run_id
			WHERE r.is_live AND r.symbol = $1 AND r.interval = $2 AND pt.entry_time >= $3
		) s`, cfg.Symbol, cfg.Interval, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[string]bool)
	for rows.Next() {
		var strat, side string
		var entry, sl, tp float64
		if err := rows.Scan(&strat, &side, &entry, &sl, &tp); err != nil {
			return nil, err
		}
		seen[signalKey(strat, side, entry, sl, tp)] = true
	}
	return seen, rows.Err()
}

// replayMissedSignals scans for ENTRY-tier signals (|NetWeight| ≥ 2.0)
// that closed in the window [last_paper_run.ended_at, now], capped at
// the most recent 24h. Emits one MISSED log line per match without
// touching paper_pending — purely informational so the operator sees
// what fired while the script was offline. Also warms the dynamic
// threshold buffer via the shared compose closure.
const (
	replayMaxWindow    = 24 * time.Hour
	replayEntryNetAbs  = 2.0
	replayFetchPadding = 10
)

func replayMissedSignals(ctx context.Context, pool *pgxpool.Pool, cfg LiveConfig,
	compose func([]core.Candle, smc.Analysis, int) meta.CompositeSignal,
	params engine.BacktestParams, intervalDur time.Duration, now time.Time, log Logger) error {

	var lastEnded *time.Time
	err := pool.QueryRow(ctx, `
		SELECT MAX(ended_at) FROM paper_runs
		WHERE symbol = $1 AND interval = $2 AND is_live = true`,
		cfg.Symbol, cfg.Interval).Scan(&lastEnded)
	if err != nil {
		return fmt.Errorf("query last paper_run: %w", err)
	}
	if lastEnded == nil {
		log.Log("replay: no prior paper_runs for %s %s — skipping missed-signal digest", cfg.Symbol, cfg.Interval)
		return nil
	}

	gap := now.Sub(*lastEnded)
	if gap <= intervalDur {
		log.Log("replay: gap %s ≤ interval %s — nothing to replay", gap.Round(time.Second), intervalDur)
		return nil
	}
	return scanGreenSignals(ctx, pool, cfg, compose, params, intervalDur, *lastEnded, now, "since-last-shutdown", log)
}

// ReplayGreens prints a digest of ENTRY-tier signals (|net|≥2.0) for the
// last `window` of closed bars and returns. Pure read-only — never touches
// paper_pending or paper_runs. `window` is capped at 24h to match
// TwelveData free-tier history limits.
func ReplayGreens(ctx context.Context, pool *pgxpool.Pool, cfg LiveConfig,
	weights map[string]float64, weightsBySession map[string]map[string]float64,
	window time.Duration, log Logger) error {

	if window <= 0 {
		return fmt.Errorf("ReplayGreens: window must be > 0")
	}
	params := buildLiveParams(cfg)
	intervalDur := intervalDuration(cfg.Interval)
	if intervalDur <= 0 {
		return fmt.Errorf("unsupported interval %q", cfg.Interval)
	}
	store := newWeightStore(weights, weightsBySession, cfg.SourceRunID)
	compose := buildComposer(cfg, store)
	now := time.Now().UTC()
	return scanGreenSignals(ctx, pool, cfg, compose, params, intervalDur, now.Add(-window), now, "on-demand", log)
}

// scanGreenSignals is the shared body of replayMissedSignals and ReplayGreens.
// Walks closed bars in [from, to], recomposes each signal, and logs one
// `MISSED` line per match with |NetWeight| ≥ replayEntryNetAbs.
func scanGreenSignals(ctx context.Context, pool *pgxpool.Pool, cfg LiveConfig,
	compose func([]core.Candle, smc.Analysis, int) meta.CompositeSignal,
	params engine.BacktestParams, intervalDur time.Duration,
	from, to time.Time, reason string, log Logger) error {

	// from may arrive as paper_runs.ended_at in pgx-local location; normalize
	// so the "scanning … UTC" banner shows true UTC. Instant-preserving.
	from, to = from.UTC(), to.UTC()

	if !to.After(from) {
		log.Log("replay: range [%s → %s] is empty — nothing to scan",
			from.Format("2006-01-02 15:04 UTC"), to.Format("2006-01-02 15:04 UTC"))
		return nil
	}
	capped := false
	if to.Sub(from) > replayMaxWindow {
		from = to.Add(-replayMaxWindow)
		capped = true
	}
	gap := to.Sub(from)
	if capped {
		log.Log("replay: scanning ENTRY signals (|net|≥%.1f) %s → %s (%s, 24h cap)",
			replayEntryNetAbs,
			from.Format("2006-01-02 15:04 UTC"), to.Format("2006-01-02 15:04 UTC"), reason)
	} else {
		log.Log("replay: scanning ENTRY signals (|net|≥%.1f) %s → %s (%s, gap %s)",
			replayEntryNetAbs,
			from.Format("2006-01-02 15:04 UTC"), to.Format("2006-01-02 15:04 UTC"),
			reason, gap.Round(time.Minute))
	}

	barsNeeded := int(gap/intervalDur) + replayFetchPadding
	fetched, err := livefeed.FetchLatest(ctx, cfg.HTTPClient, cfg.APIKey, cfg.Symbol, cfg.Interval, barsNeeded)
	if err != nil {
		log.Log("replay: fetch error: %v — falling back to DB-only candles", err)
	} else if len(fetched) > 0 {
		if _, err := upsertOHLC(ctx, pool, cfg.Symbol, cfg.Interval, fetched); err != nil {
			log.Log("replay: upsert error: %v", err)
		}
	}

	candles, err := loadAllCandles(ctx, pool, cfg.Symbol, cfg.Interval)
	if err != nil {
		return fmt.Errorf("load candles: %w", err)
	}
	if len(candles) < params.MinIndex+1 {
		log.Log("replay: only %d candles available — need %d, skipping replay", len(candles), params.MinIndex+1)
		return nil
	}

	lastClosedIdx := closedBarIndex(candles, intervalDur, to)
	if lastClosedIdx < 0 {
		log.Log("replay: no closed bars available")
		return nil
	}

	a := smc.Analyze(candles)
	found := 0
	for i := params.MinIndex; i <= lastClosedIdx; i++ {
		bt := candles[i].Time
		if !bt.After(from) {
			continue
		}
		cs := compose(candles, a, i)
		if math.Abs(cs.NetWeight) < replayEntryNetAbs {
			continue
		}
		strat := cs.WinnerStrategy
		if strat == "" {
			strat = "Composite"
		}
		log.Log("MISSED  %s  %s %s  net=%+.2f  vote=%.2f  close=$%.2f  entry=%.2f  SL=%.2f  TP=%.2f",
			bt.Format("2006-01-02 15:04 UTC"), cs.Side.String(), strat,
			cs.NetWeight, cs.AppliedThreshold, candles[i].Close,
			cs.BestEntry, cs.BestSL, cs.BestTP)
		found++
	}
	if found == 0 {
		log.Log("replay: no ENTRY-tier signals in window")
	} else {
		log.Log("replay: %d ENTRY signal(s) in window", found)
	}
	return nil
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
		// Normalize pgx's local-location timestamptz to UTC so BAR/MISSED logs
		// print true UTC (matching the WhatsApp alert). Instant-preserving.
		c.Time = c.Time.UTC()
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
