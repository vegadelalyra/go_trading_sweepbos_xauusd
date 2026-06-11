package paper

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/engine"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/meta"
)

// shadowPosition mirrors a shadow_trades row with status='open'. Same shape
// as pendingPosition plus the rejecter+gate+reason context captured at the
// moment acceptSignal said "no". Loaded once per bar from DB so the loop
// is restart-resilient — a crashed paper-live picks up open shadows just
// like it picks up open pendings.
type shadowPosition struct {
	ID         int64
	Rejecter   string
	RejectGate string
	Reason     string
	Strategy   string
	Side       core.Side
	EntryTime  time.Time
	Entry      float64
	SL         float64
	TP         float64
}

// classifyRejectGate maps the human-readable reason string returned by
// acceptSignal onto the stable gate code persisted in shadow_trades. Kept
// in lockstep with the reason templates in live.go:acceptSignal — if a new
// reason ever ships, this returns "" and the caller should NOT record a
// shadow (defensive: better lose one shadow than corrupt aggregations).
func classifyRejectGate(reason string) string {
	switch {
	case strings.HasPrefix(reason, "H4 bias"):
		return "BIAS"
	case strings.HasPrefix(reason, "PD:"):
		return "PD"
	case strings.HasPrefix(reason, "MaxSL:"):
		return "MAXSL"
	default:
		return ""
	}
}

// recordShadow inserts a shadow_trades row in status='open' using the same
// entry/SL/TP that acceptSignal computed before rejecting. The spread is
// intentionally NOT baked in here: shadows audit the structural signal the
// strategy proposed, so we use cs.BestEntry directly. Returns the new id
// for log correlation. Silent no-op when classifyRejectGate returns ""
// (unknown reason format) — see that function for rationale.
func recordShadow(ctx context.Context, pool *pgxpool.Pool, paperRunID int64,
	cs meta.CompositeSignal, reason string, entryTime time.Time) (int64, error) {

	gate := classifyRejectGate(reason)
	if gate == "" {
		return 0, nil
	}
	strat := cs.WinnerStrategy
	if strat == "" {
		strat = "Composite"
	}

	var id int64
	err := pool.QueryRow(ctx, `
		INSERT INTO shadow_trades (
			paper_run_id, rejecter, reject_gate, reject_reason,
			strategy, side,
			entry_time, entry_price, sl, tp,
			net_weight, applied_threshold,
			session, year_month, year_quarter
		) VALUES (
			$1, $2, $3, $4,
			$5, $6,
			$7, $8, $9, $10,
			$11, $12,
			$13, $14, $15
		) RETURNING id`,
		paperRunID, cs.WinnerStrategy, gate, reason,
		strat, cs.Side.String(),
		entryTime.UTC(), cs.BestEntry, cs.BestSL, cs.BestTP,
		cs.NetWeight, cs.AppliedThreshold,
		core.ClassifySession(entryTime),
		core.YearMonthBucket(entryTime),
		core.YearQuarterBucket(entryTime),
	).Scan(&id)
	return id, err
}

// loadOpenShadows returns every shadow_trades row with status='open' for
// this run, ordered by entry_time so logs stay deterministic. Mirrors
// loadPending — same restart-safety story.
func loadOpenShadows(ctx context.Context, pool *pgxpool.Pool, paperRunID int64) ([]shadowPosition, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, rejecter, reject_gate, reject_reason, strategy, side,
		       entry_time, entry_price, sl, tp
		FROM shadow_trades
		WHERE paper_run_id = $1 AND status = 'open'
		ORDER BY entry_time`, paperRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []shadowPosition
	for rows.Next() {
		var s shadowPosition
		var sideStr string
		if err := rows.Scan(&s.ID, &s.Rejecter, &s.RejectGate, &s.Reason, &s.Strategy, &sideStr,
			&s.EntryTime, &s.Entry, &s.SL, &s.TP); err != nil {
			return nil, err
		}
		if sideStr == "SHORT" {
			s.Side = core.Short
		} else {
			s.Side = core.Long
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// gradeShadow walks candles AFTER pos.EntryTime via engine.AdvanceTrade —
// identical lifecycle to gradeOne (the paper_pending grader). Returns nil,
// false when the position is still in flight.
func gradeShadow(pos shadowPosition, candles []core.Candle, p engine.BacktestParams) (*core.Trade, bool) {
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

// closeShadow flips status='open' → 'closed' and populates exit_* columns
// in a single UPDATE. No row movement (single-table design), so no TX
// needed — the UPDATE is atomic on its own.
func closeShadow(ctx context.Context, pool *pgxpool.Pool, shadowID int64, t core.Trade) error {
	_, err := pool.Exec(ctx, `
		UPDATE shadow_trades SET
			status          = 'closed',
			exit_time       = $1,
			exit_price      = $2,
			r_realized      = $3,
			win             = $4,
			candles_to_exit = $5
		WHERE id = $6`,
		t.ExitTime.UTC(), t.ExitPrice, t.R, t.Win, t.CandlesToExit,
		shadowID,
	)
	return err
}

