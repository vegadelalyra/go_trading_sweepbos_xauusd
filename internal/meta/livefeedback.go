package meta

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Live-feedback defaults (Tema 2). The shrinkage constant K sets how fast the
// live estimate earns weight: λ = n/(n+K), so K clean live trades for a
// strategy give the live opinion a 50% say. K=200 is deliberately conservative
// — the canonical dominates until a genuinely large clean live sample
// accumulates (greens are slow: ~10/12 days). Floor is a hard minimum below
// which a strategy's live trades are ignored entirely (pure small-sample
// noise), complementing the continuous shrinkage above it.
const (
	DefaultLiveFeedbackK     = 200.0
	DefaultLiveFeedbackFloor = 20
)

// LiveFeedbackConfig parametrises blending clean live paper-trade outcomes into
// the canonical weights. Zero-value (Enabled=false) keeps the canonical-only
// behaviour bit-for-bit. Since excludes pre-dedup-fix live trades, which stacked
// structural duplicates and would over-count losses (see the Tema 1 fix); the
// CLI defaults it to the fix's deploy date so corrupt history never feeds back.
type LiveFeedbackConfig struct {
	Enabled  bool
	Symbol   string
	Interval string
	Since    time.Time
	K        float64
	Floor    int
}

// LoadLiveWeights aggregates per-strategy stats from CLEAN live paper_trades
// (is_live runs of the given symbol/interval with entry_time >= since) and
// returns one StrategyWeight per strategy that traded, using the same
// variance-aware SharpeWeight as the canonical path. Unlike LoadWeights it
// applies NO alpha-decay silencing — it is a pure live estimate. Decay stays a
// canonical-only decision (see BlendLiveWeights) so a small live sample can
// never silence a strategy.
func LoadLiveWeights(ctx context.Context, pool *pgxpool.Pool, symbol, interval string, since time.Time) ([]StrategyWeight, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			pt.strategy,
			COUNT(*)::int                                               AS trades,
			AVG(pt.r_realized)::float8                                  AS expectancy,
			COALESCE(STDDEV_POP(pt.r_realized), 0)::float8             AS stddev_r,
			(COUNT(*) FILTER (WHERE pt.win))::float8 / COUNT(*)::float8 AS winrate,
			SUM(pt.r_realized)::float8                                  AS net_r
		FROM paper_trades pt
		JOIN paper_runs r ON r.id = pt.paper_run_id
		WHERE r.is_live AND r.symbol = $1 AND r.interval = $2 AND pt.entry_time >= $3
		GROUP BY pt.strategy
		ORDER BY pt.strategy
	`, symbol, interval, since.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StrategyWeight
	for rows.Next() {
		var sw StrategyWeight
		if err := rows.Scan(&sw.Strategy, &sw.Trades, &sw.Expectancy, &sw.StdDev, &sw.WinRate, &sw.NetR); err != nil {
			return nil, err
		}
		sw.Weight = SharpeWeight(sw.Expectancy, sw.StdDev, sw.Trades)
		out = append(out, sw)
	}
	return out, rows.Err()
}

// BlendLiveWeights shrinks each canonical weight toward its live estimate:
//
//	w_final = (1-λ)·w_canon + λ·w_live,   λ = n_live / (n_live + k)
//
// Guards that keep a small or adverse live sample from doing damage:
//   - A strategy the canonical does NOT endorse (w_canon <= 0, whether from
//     negative expectancy or alpha-decay silencing) is never resurrected by
//     live data — w_final stays 0. The blend only re-scales strategies the
//     long-run backtest already trusts.
//   - A strategy with fewer than `floor` clean live trades keeps its canonical
//     weight untouched (pure small-sample noise is ignored, not shrunk).
//
// Pure over the two slices so it is unit-testable without a DB. The result has
// exactly the canonical strategies, in canonical order (live-only strategies
// are dropped — see the no-resurrect guard).
func BlendLiveWeights(canon, live []StrategyWeight, k float64, floor int) []StrategyWeight {
	if k <= 0 {
		k = DefaultLiveFeedbackK
	}
	liveBy := make(map[string]StrategyWeight, len(live))
	for _, lw := range live {
		liveBy[lw.Strategy] = lw
	}
	out := make([]StrategyWeight, len(canon))
	copy(out, canon)
	for i := range out {
		cw := out[i].Weight
		if cw <= 0 {
			continue // never resurrect a strategy the canonical doesn't endorse
		}
		lw, ok := liveBy[out[i].Strategy]
		if !ok || lw.Trades < floor {
			continue // not enough clean live evidence — keep canonical weight
		}
		lambda := float64(lw.Trades) / (float64(lw.Trades) + k)
		out[i].Weight = (1-lambda)*cw + lambda*lw.Weight
	}
	return out
}

// LoadWeightsBlended is the canonical+live entry point used by paper-live's
// startup load and its daily auto-refresh when live feedback is enabled. It
// derives the canonical weights (with alpha-decay applied) then shrinks them
// toward the clean live estimate per fb. Callers guard fb.Enabled to keep the
// legacy canonical-only path bit-for-bit identical when it is off.
func LoadWeightsBlended(ctx context.Context, pool *pgxpool.Pool, runID int64, fb LiveFeedbackConfig) ([]StrategyWeight, error) {
	canon, err := LoadWeights(ctx, pool, runID)
	if err != nil {
		return nil, err
	}
	live, err := LoadLiveWeights(ctx, pool, fb.Symbol, fb.Interval, fb.Since)
	if err != nil {
		return nil, err
	}
	floor := fb.Floor
	if floor <= 0 {
		floor = DefaultLiveFeedbackFloor
	}
	return BlendLiveWeights(canon, live, fb.K, floor), nil
}
