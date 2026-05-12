// Package meta owns the meta-strategy layer: per-strategy weights
// derived from a stored backtest run, the variance-aware Sharpe weight
// formula, alpha-decay detection, and the composite-vote engine that
// turns every strategy's signal on a given candle into a single
// recommended entry. It depends on core (Candle/Side/Trade), smc
// (Analysis) and the strategy catalog — never on engine, wfa, cli or db.
package meta

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// StrategyWeight summarises a strategy's historical edge in a single number.
//
// Formula:  weight = max(0, expectancy_R / stddev_R) * log(trades + 1)
//
//   - Numerator is a per-trade Sharpe analogue — expectancy normalised by
//     realised dispersion. Two strategies with the same expectancy but one
//     with 10× the variance receive very different weights.
//   - Negative expectancy → ratio <= 0 → weight 0, the strategy doesn't vote.
//   - log(N+1) compresses the bonus given to hyperactive over-traders. The
//     previous sqrt(N) let a 1000-trade strategy carry 30× more weight than
//     a 1-trade one even when the per-trade quality was identical; log
//     dampens that to ~7×.
//
// Replaces the original max(0, expectancy) * sqrt(trades) formula, which
// was variance-blind: fat-tail winners with long loss streaks got the same
// weight as a steady-eddy strategy with the same average R.
type StrategyWeight struct {
	Strategy   string
	Trades     int
	Expectancy float64
	StdDev     float64
	WinRate    float64
	NetR       float64
	Weight     float64
}

// LoadWeights aggregates per-strategy stats from a stored backtest run and
// returns one StrategyWeight per strategy that traded in that run. The
// returned slice carries the variance-aware weight described on StrategyWeight.
//
// When `paperRunIDs` is non-empty, trades from the named paper_runs are
// UNION-ed into the aggregation — i.e. graded paper-trading outcomes
// influence the weights alongside the canonical backtest. This is the
// feedback loop: as paper trading accumulates, the weights drift toward
// reflecting live conditions (spread, slippage, regime drift) instead of
// only the static backtest. Pass nil/empty to keep the legacy behaviour.
func LoadWeights(ctx context.Context, pool *pgxpool.Pool, runID int64, paperRunIDs ...int64) ([]StrategyWeight, error) {
	rows, err := pool.Query(ctx, `
		WITH all_trades AS (
			SELECT strategy, r_realized, win
			FROM backtest_trades
			WHERE run_id = $1
			UNION ALL
			SELECT strategy, r_realized, win
			FROM paper_trades
			WHERE paper_run_id = ANY($2)
		)
		SELECT
			strategy,
			COUNT(*)::int                                              AS trades,
			AVG(r_realized)::float8                                    AS expectancy,
			COALESCE(STDDEV_POP(r_realized), 0)::float8                AS stddev_r,
			(COUNT(*) FILTER (WHERE win))::float8 / COUNT(*)::float8   AS winrate,
			SUM(r_realized)::float8                                    AS net_r
		FROM all_trades
		GROUP BY strategy
		ORDER BY strategy
	`, runID, paperRunIDs)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Force-silence strategies whose recent Profit Factor has decayed —
	// long-term edge alone is not enough if the latest window says the
	// market has changed. Decay detection consults the same combined
	// (backtest + paper) trade stream so paper outcomes can both lift AND
	// kill a strategy's weight.
	decayed, err := DetectAlphaDecay(ctx, pool, runID, DefaultDecayWindowTrades, paperRunIDs...)
	if err != nil {
		return nil, fmt.Errorf("detect alpha decay: %w", err)
	}
	for i := range out {
		if decayed[out[i].Strategy] {
			out[i].Weight = 0
		}
	}
	return out, nil
}

// SharpeWeight implements the variance-aware weight formula used by both
// LoadWeights (SQL aggregation) and wfa's in-memory equivalent.
// Single source of truth — drift between the two would silently bias WFA.
func SharpeWeight(expectancy, stddev float64, trades int) float64 {
	if stddev <= 1e-9 || expectancy <= 0 || trades <= 0 {
		return 0
	}
	return (expectancy / stddev) * math.Log(float64(trades+1))
}

// WeightMap converts a slice into a name → weight lookup for ComposeSignal.
func WeightMap(ws []StrategyWeight) map[string]float64 {
	m := make(map[string]float64, len(ws))
	for _, w := range ws {
		m[w.Strategy] = w.Weight
	}
	return m
}

// LoadWeightsBySession returns weights keyed by [strategy][session]. The
// motivation is that a strategy's edge often concentrates in a specific
// session (BollingerRev can be strong in Asia but a coin flip in NY-AM);
// a single global weight smears those facts together. Sessions without
// enough samples (< MinPerSessionTrades) are dropped — partial coverage
// is better than weighting on noise.
//
// The map is sparse: ComposeSignalBySession reads weights[strat][sess] and
// treats a missing entry as 0 (= strategy silent in that session).
func LoadWeightsBySession(ctx context.Context, pool *pgxpool.Pool, runID int64, paperRunIDs ...int64) (map[string]map[string]float64, error) {
	rows, err := pool.Query(ctx, `
		WITH all_trades AS (
			SELECT strategy, session, r_realized
			FROM backtest_trades
			WHERE run_id = $1
			UNION ALL
			SELECT strategy, session, r_realized
			FROM paper_trades
			WHERE paper_run_id = ANY($3)
		)
		SELECT
			strategy,
			session,
			COUNT(*)::int                                AS trades,
			AVG(r_realized)::float8                      AS expectancy,
			COALESCE(STDDEV_POP(r_realized), 0)::float8  AS stddev_r
		FROM all_trades
		GROUP BY strategy, session
		HAVING COUNT(*) >= $2
		ORDER BY strategy, session
	`, runID, MinPerSessionTrades, paperRunIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]map[string]float64{}
	for rows.Next() {
		var strat, sess string
		var trades int
		var exp, stddev float64
		if err := rows.Scan(&strat, &sess, &trades, &exp, &stddev); err != nil {
			return nil, err
		}
		w := SharpeWeight(exp, stddev, trades)
		if w <= 0 {
			continue
		}
		if out[strat] == nil {
			out[strat] = map[string]float64{}
		}
		out[strat][sess] = w
	}
	return out, rows.Err()
}

// MinPerSessionTrades is the floor below which a (strategy, session) cell
// is judged too sparse to weight reliably. Tuned for the 2y dataset; in
// a longer history this could climb without losing too many cells.
const MinPerSessionTrades = 10

// DecayThreshold is the fraction of historic Profit Factor below which a
// strategy is judged to have lost its edge in the recent window.
// 0.6 ≈ "two-thirds of the original edge has evaporated".
const DecayThreshold = 0.6

// DefaultDecayWindowTrades is the count of most-recent trades that define
// the "recent" window for alpha-decay detection. 80 is empirically a good
// balance — short enough to react to regime shifts within a quarter,
// long enough to filter noise on lower-frequency strategies.
const DefaultDecayWindowTrades = 80

// DetectAlphaDecay returns the names of strategies whose recent Profit
// Factor has dropped below DecayThreshold × their long-term Profit Factor
// in the same backtest run. Strategies need at least 2× windowTrades of
// history before they are eligible — otherwise the "historic" baseline is
// indistinguishable from the recent window.
//
// LoadWeights consults this set and force-silences any strategy that
// appears in it (weight = 0) regardless of cumulative expectancy. Without
// this guard, a strategy that traded profitably for a year and then drifted
// into a six-month rut keeps voting in the composite at its long-term weight.
func DetectAlphaDecay(ctx context.Context, pool *pgxpool.Pool, runID int64, windowTrades int, paperRunIDs ...int64) (map[string]bool, error) {
	if windowTrades <= 0 {
		windowTrades = DefaultDecayWindowTrades
	}
	rows, err := pool.Query(ctx, `
		WITH all_trades AS (
			SELECT strategy, r_realized, entry_time
			FROM backtest_trades
			WHERE run_id = $1
			UNION ALL
			SELECT strategy, r_realized, entry_time
			FROM paper_trades
			WHERE paper_run_id = ANY($3)
		),
		numbered AS (
			SELECT strategy, r_realized,
				ROW_NUMBER() OVER (PARTITION BY strategy ORDER BY entry_time DESC) AS rn
			FROM all_trades
		),
		recent AS (
			SELECT strategy,
				SUM(CASE WHEN r_realized > 0 THEN r_realized      ELSE 0 END) AS gross_win,
				SUM(CASE WHEN r_realized < 0 THEN -r_realized     ELSE 0 END) AS gross_loss
			FROM numbered
			WHERE rn <= $2
			GROUP BY strategy
		),
		historic AS (
			SELECT strategy,
				SUM(CASE WHEN r_realized > 0 THEN r_realized  ELSE 0 END) /
					NULLIF(SUM(CASE WHEN r_realized < 0 THEN -r_realized ELSE 0 END), 0) AS pf,
				COUNT(*) AS n
			FROM numbered
			GROUP BY strategy
			HAVING COUNT(*) >= $2 * 2
		)
		SELECT h.strategy, h.pf, (r.gross_win / NULLIF(r.gross_loss, 0)) AS recent_pf
		FROM historic h JOIN recent r USING (strategy)
		WHERE r.gross_loss > 0
	`, runID, windowTrades, paperRunIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	decayed := map[string]bool{}
	for rows.Next() {
		var strat string
		var histPF, recentPF float64
		if err := rows.Scan(&strat, &histPF, &recentPF); err != nil {
			return nil, err
		}
		// Only flag if the historic edge was real to begin with.
		if histPF > 1.0 && recentPF < histPF*DecayThreshold {
			decayed[strat] = true
		}
	}
	return decayed, rows.Err()
}

// CompositeSignal is the meta-strategy's verdict for one candle. It always
// reports both sides' weights so the caller sees how decisive the vote was.
type CompositeSignal struct {
	Side        core.Side // chosen direction if Recommended
	LongWeight  float64   // sum of weights of strategies that flagged LONG
	ShortWeight float64   // ditto for SHORT
	NetWeight   float64   // LongWeight − ShortWeight
	Confidence  float64   // |NetWeight| / (LongWeight + ShortWeight); 0..1
	Recommended bool      // true if |NetWeight| crosses threshold
	// AppliedThreshold is the gate the composer compared |NetWeight|
	// against. For ComposeSignal it's the caller's static threshold; for
	// ComposeSignalDynamic it's the percentile-derived value at this bar.
	// Logged so a downstream operator sees both "how strong was the vote"
	// AND "how close was it to firing".
	AppliedThreshold float64
	Triggers         []TaggedSignal
	BestEntry        float64
	BestSL           float64
	BestTP           float64
	// WinnerStrategy names the strategy whose Entry/SL/TP were chosen — i.e.
	// the highest-weighted contributor on the winning side. Empty when
	// Recommended is false. Critical for attribution: composite trades that
	// hit are credited back to the actual strategy that supplied the levels,
	// so meta.LoadWeights aggregates real per-strategy edges rather than
	// lumping every composite-mediated trade under a single "Composite" key.
	WinnerStrategy string
}

// TaggedSignal pairs a raw strategy signal with the weight that strategy
// carries in this composite vote — useful for debugging which voices won.
type TaggedSignal struct {
	Signal core.Signal
	Weight float64
}

// composeCore aggregates Long/Short weights for every triggering strategy
// at candle i without making the Recommended decision. Threshold gating is
// applied by callers via applyThreshold, which makes both the fixed-threshold
// path (ComposeSignal) and the percentile-threshold path (ComposeSignalDynamic)
// share one codepath. weightOf is the lookup that lets the same core power
// both the flat-weights and per-session callers.
//
// The LondonFixConfig argument adjusts the per-strategy weight at vote time
// when the candle falls inside the configured fix window. Default zero-value
// (Enabled=false) is a no-op — every existing caller path stays bit-for-bit
// identical until London Fix is explicitly enabled.
//
// The COMEXPinConfig argument is the Phase 3.2 sibling overlay: it silences
// momentum/breakout strategies and boosts mean-reversion ones inside the
// 48h pre-expiry window around the seeded GC option expiry dates. Same
// "zero-value is a no-op" contract as LondonFixConfig — both adjusters are
// composable; lf runs first, then comex on the lf-adjusted weight.
//
// Returns the in-progress CompositeSignal plus the best-weighted long and
// short triggers (for applyThreshold to promote into the recommended levels).
func composeCore(candles []core.Candle, a smc.Analysis, i int, weightOf func(strat, sess string) float64, lf LondonFixConfig, comex COMEXPinConfig) (CompositeSignal, *TaggedSignal, *TaggedSignal) {
	cs := CompositeSignal{}
	var bestLong, bestShort *TaggedSignal
	sess := core.ClassifySession(candles[i].Time)
	candleTime := candles[i].Time
	for _, s := range strategy.All() {
		sig, ok := s.Detect(candles, a, i)
		if !ok {
			continue
		}
		w := weightOf(sig.Strategy, sess)
		// London Fix adjustment is applied AFTER the base weight lookup
		// so a zero base weight stays zero and a non-zero one is scaled
		// only inside the 14:40-15:20 UTC window (see LondonFixConfig).
		w = lf.adjust(w, sig.Strategy, candleTime)
		// COMEX pinning adjustment composes on top of the lf-adjusted
		// weight. Order matters only if both overlays touch the SAME
		// strategy, which the current classifiers (IsLondonFixMomentum,
		// IsLiquidityFade, IsMeanReversion) deliberately keep disjoint —
		// so the two overlays are commutative in practice.
		w = comex.adjust(w, sig.Strategy, candleTime)
		if w <= 0 {
			continue
		}
		ts := TaggedSignal{Signal: sig, Weight: w}
		cs.Triggers = append(cs.Triggers, ts)
		if sig.Side == core.Long {
			cs.LongWeight += w
			if bestLong == nil || w > bestLong.Weight {
				bestLong = &ts
			}
		} else {
			cs.ShortWeight += w
			if bestShort == nil || w > bestShort.Weight {
				bestShort = &ts
			}
		}
	}
	cs.NetWeight = cs.LongWeight - cs.ShortWeight
	total := cs.LongWeight + cs.ShortWeight
	if total > 0 {
		cs.Confidence = math.Abs(cs.NetWeight) / total
	}
	return cs, bestLong, bestShort
}

// applyThreshold promotes bestLong or bestShort into the recommended slot
// when |NetWeight| crosses threshold. Mutates cs in place so the same
// CompositeSignal struct returned from composeCore is the one the caller
// receives.
func applyThreshold(cs *CompositeSignal, bestLong, bestShort *TaggedSignal, threshold float64) {
	cs.AppliedThreshold = threshold
	if cs.NetWeight >= threshold && bestLong != nil {
		cs.Side = core.Long
		cs.Recommended = true
		cs.BestEntry = bestLong.Signal.Entry
		cs.BestSL = bestLong.Signal.SL
		cs.BestTP = bestLong.Signal.TP
		cs.WinnerStrategy = bestLong.Signal.Strategy
	} else if -cs.NetWeight >= threshold && bestShort != nil {
		cs.Side = core.Short
		cs.Recommended = true
		cs.BestEntry = bestShort.Signal.Entry
		cs.BestSL = bestShort.Signal.SL
		cs.BestTP = bestShort.Signal.TP
		cs.WinnerStrategy = bestShort.Signal.Strategy
	}
}

// ComposeSignal evaluates every strategy on the given candle and folds their
// signals into a weighted vote. If the absolute net weight clears
// threshold, it picks the direction and surfaces the best (highest-weight)
// individual entry/SL/TP triplet from the winning side as the recommended
// execution levels.
//
// The trailing LondonFixConfig and COMEXPinConfig (zero-values = no-op)
// let callers enable the 15:00 GMT fix-window adjustment and the 48h
// COMEX pre-expiry pinning adjustment respectively, without touching the
// return shape.
func ComposeSignal(candles []core.Candle, a smc.Analysis, i int, weights map[string]float64, threshold float64, lf LondonFixConfig, comex COMEXPinConfig) CompositeSignal {
	cs, bL, bS := composeCore(candles, a, i, func(strat, _ string) float64 {
		return weights[strat]
	}, lf, comex)
	applyThreshold(&cs, bL, bS, threshold)
	return cs
}

// ComposeSignalBySession is the per-session-aware companion to ComposeSignal.
// Instead of a flat weights map it reads weightsBySession[strategy][session]
// using the candle's classified session — so BollingerRev can vote in Asia
// while staying silent in NY-AM, and vice versa.
//
// Same threshold and best-entry tie-break as ComposeSignal; only the weight
// lookup differs. The trailing LondonFixConfig and COMEXPinConfig are
// identical in semantics.
func ComposeSignalBySession(candles []core.Candle, a smc.Analysis, i int, weightsBySession map[string]map[string]float64, threshold float64, lf LondonFixConfig, comex COMEXPinConfig) CompositeSignal {
	cs, bL, bS := composeCore(candles, a, i, func(strat, sess string) float64 {
		if perSess, ok := weightsBySession[strat]; ok {
			return perSess[sess]
		}
		return 0
	}, lf, comex)
	applyThreshold(&cs, bL, bS, threshold)
	return cs
}

// ComposeSignalDynamic is the percentile-threshold companion to ComposeSignal.
// Instead of a hard-coded threshold it derives the gate at each call from
// the upper-tail percentile of |NetWeight| observations stored in buf —
// so the system trades the same upper fraction of decisive setups regardless
// of regime. Active observations (where at least one strategy fired) are
// pushed; quiet candles are skipped so the percentile reflects active-signal
// strength, not noise.
//
// When buf is empty the threshold collapses to 0 and the first non-zero
// observation will trigger; subsequent calls progressively raise the gate
// as the empirical distribution fills in. Callers concerned about cold-start
// should warm the buffer (e.g., on the IS slice of a WFA window) before
// gating any decision.
//
// The trailing LondonFixConfig and COMEXPinConfig (zero-values = no-op)
// let callers enable the 15:00 GMT fix-window adjustment and the 48h
// COMEX pre-expiry pinning adjustment respectively.
func ComposeSignalDynamic(candles []core.Candle, a smc.Analysis, i int, weights map[string]float64, percentile float64, buf *ThresholdBuffer, lf LondonFixConfig, comex COMEXPinConfig) CompositeSignal {
	cs, bL, bS := composeCore(candles, a, i, func(strat, _ string) float64 {
		return weights[strat]
	}, lf, comex)
	if buf != nil && (cs.LongWeight+cs.ShortWeight) > 0 {
		buf.Push(math.Abs(cs.NetWeight))
	}
	threshold := 0.0
	if buf != nil {
		threshold = buf.Percentile(percentile)
	}
	applyThreshold(&cs, bL, bS, threshold)
	return cs
}

// ComposeSignalBySessionDynamic is the per-session-aware companion to
// ComposeSignalDynamic — same percentile-gate behavior, per-session weight
// lookup. See ComposeSignalBySession for the rationale of per-session weights.
func ComposeSignalBySessionDynamic(candles []core.Candle, a smc.Analysis, i int, weightsBySession map[string]map[string]float64, percentile float64, buf *ThresholdBuffer, lf LondonFixConfig, comex COMEXPinConfig) CompositeSignal {
	cs, bL, bS := composeCore(candles, a, i, func(strat, sess string) float64 {
		if perSess, ok := weightsBySession[strat]; ok {
			return perSess[sess]
		}
		return 0
	}, lf, comex)
	if buf != nil && (cs.LongWeight+cs.ShortWeight) > 0 {
		buf.Push(math.Abs(cs.NetWeight))
	}
	threshold := 0.0
	if buf != nil {
		threshold = buf.Percentile(percentile)
	}
	applyThreshold(&cs, bL, bS, threshold)
	return cs
}

// PrintCompositeSignal writes a CLI-friendly report of the meta-strategy
// verdict for a single candle. If capital > 0, also computes Admirals
// position sizing (1 lot = 100 oz, Tier 1 leverage 1:500).
func PrintCompositeSignal(symbol, interval string, candle core.Candle, cs CompositeSignal, runID int64, threshold, capital float64) {
	fmt.Printf("\n═══ Composite Signal — %s %s ═══\n", symbol, interval)
	fmt.Printf("  Run: #%d  ·  Threshold: %.2f  ·  Last candle: %s  ·  Close: $%.2f\n\n",
		runID, threshold,
		candle.Time.Format("2006-01-02 15:04 MST"),
		candle.Close)

	if len(cs.Triggers) == 0 {
		fmt.Println("  No strategy fired on this candle. → NO TRADE")
		return
	}

	fmt.Println("── Triggers (this candle) ──────────────────────────────────")
	fmt.Printf("  %-18s  %-6s  %-8s  %-8s  %-8s  %-8s\n",
		"Strategy", "Side", "Entry", "SL", "TP", "Weight")
	for _, t := range cs.Triggers {
		fmt.Printf("  %-18s  %-6s  %8.2f  %8.2f  %8.2f  %8.3f\n",
			t.Signal.Strategy, t.Signal.Side.String(),
			t.Signal.Entry, t.Signal.SL, t.Signal.TP, t.Weight)
	}

	fmt.Println()
	fmt.Printf("  Long weight:  %7.3f\n", cs.LongWeight)
	fmt.Printf("  Short weight: %7.3f\n", cs.ShortWeight)
	fmt.Printf("  Net:          %+7.3f  (confidence %.1f%%)\n", cs.NetWeight, cs.Confidence*100)
	fmt.Println()

	if !cs.Recommended {
		fmt.Printf("  Verdict: ⏸  NO TRADE — |net| %.3f < threshold %.2f\n",
			math.Abs(cs.NetWeight), threshold)
		return
	}

	emoji := "🟢"
	if cs.Side == core.Short {
		emoji = "🔴"
	}
	d := math.Abs(cs.BestEntry - cs.BestSL)
	rrFull := math.Abs(cs.BestTP-cs.BestEntry) / d

	fmt.Printf("  Verdict: %s  %s  — taking best-weighted entry from %s side\n",
		emoji, cs.Side.String(), cs.Side.String())
	fmt.Printf("    Entry: %.2f\n", cs.BestEntry)
	fmt.Printf("    SL:    %.2f  (D = %.2f points)\n", cs.BestSL, d)
	fmt.Printf("    TP:    %.2f  (R:R 1:%.2f)\n", cs.BestTP, rrFull)

	if capital > 0 {
		if d <= 0 {
			fmt.Println("    ⚠ SL distance is zero — cannot size position")
			return
		}
		unitsRaw := capital / d
		units := math.Floor(unitsRaw)
		if units < 1 {
			fmt.Printf("\n  ⚠ Sizing: with $%.2f and SL distance %.2f, raw=%.3f units → <1 unit unavailable on Admirals.\n",
				capital, d, unitsRaw)
			fmt.Println("    Options: tighten SL, raise capital, or use a cent account.")
			return
		}
		nocional := cs.BestEntry * units
		margin := nocional / 500
		fmt.Println()
		fmt.Println("── Admirals sizing ─────────────────────────────────────────")
		fmt.Printf("    Capital risked   $%.2f\n", capital)
		fmt.Printf("    Units (oz)       %.0f  (= %.2f MT5 lot)\n", units, units/100)
		fmt.Printf("    Real risk        $%.2f  (≤ capital ✓)\n", units*d)
		fmt.Printf("    Nominal          $%.2f\n", nocional)
		fmt.Printf("    Margin @ 1:500   $%.2f\n", margin)
		fmt.Printf("    Tier             %s\n", tierLabel(nocional))
	}
	fmt.Println()
}

// tierLabel returns the Admirals leverage tier for a given notional value.
func tierLabel(nocional float64) string {
	switch {
	case nocional <= 400_000:
		return "Tier 1 (1:500) ✓"
	case nocional <= 2_500_000:
		return "Tier 2 (1:200)"
	case nocional <= 20_000_000:
		return "Tier 3 (1:50)"
	default:
		return "Tier 4 (1:20)"
	}
}

// FormatWeights renders a weights table for the CLI / TUI.
func FormatWeights(ws []StrategyWeight) string {
	if len(ws) == 0 {
		return "no weights"
	}
	cp := append([]StrategyWeight(nil), ws...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Weight > cp[j].Weight })

	var b strings.Builder
	fmt.Fprintln(&b, "── Strategy Weights ─────────────────────────────────────────")
	fmt.Fprintf(&b, "  %-18s  %-6s  %-8s  %-7s  %-8s  %-8s  %-8s\n",
		"Strategy", "Trades", "ExpR", "σR", "WinRate", "NetR", "Weight")
	for _, w := range cp {
		fmt.Fprintf(&b, "  %-18s  %-6d  %+8.3f  %7.3f  %6.1f%%  %+8.2f  %8.3f\n",
			w.Strategy, w.Trades, w.Expectancy, w.StdDev, w.WinRate*100, w.NetR, w.Weight)
	}
	return b.String()
}
