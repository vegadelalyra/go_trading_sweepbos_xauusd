package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// StrategyWeight summarises a strategy's historical edge in a single number.
//
// Formula:  weight = max(0, expectancy_R) * sqrt(trades)
//
//   • Strategies with negative expectancy get weight 0 — they don't vote.
//   • Among the survivors, the multiplier sqrt(N) rewards strategies with
//     more trades (statistical confidence) without letting hyperactive
//     overtraders dominate linearly. This is the same Kelly-flavored
//     trick used in quant blending.
type StrategyWeight struct {
	Strategy   string
	Trades     int
	Expectancy float64
	WinRate    float64
	NetR       float64
	Weight     float64
}

// LoadWeights aggregates per-strategy stats from a stored backtest run and
// returns one StrategyWeight per strategy that traded in that run.
func LoadWeights(ctx context.Context, pool *pgxpool.Pool, runID int64) ([]StrategyWeight, error) {
	rows, err := pool.Query(ctx, `
		SELECT
			strategy,
			COUNT(*)::int                                              AS trades,
			AVG(r_realized)::float8                                    AS expectancy,
			(COUNT(*) FILTER (WHERE win))::float8 / COUNT(*)::float8   AS winrate,
			SUM(r_realized)::float8                                    AS net_r
		FROM backtest_trades
		WHERE run_id = $1
		GROUP BY strategy
		ORDER BY strategy
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []StrategyWeight
	for rows.Next() {
		var sw StrategyWeight
		if err := rows.Scan(&sw.Strategy, &sw.Trades, &sw.Expectancy, &sw.WinRate, &sw.NetR); err != nil {
			return nil, err
		}
		w := sw.Expectancy
		if w < 0 {
			w = 0
		}
		sw.Weight = w * math.Sqrt(float64(sw.Trades))
		out = append(out, sw)
	}
	return out, rows.Err()
}

// WeightMap converts a slice into a name → weight lookup for ComposeSignal.
func WeightMap(ws []StrategyWeight) map[string]float64 {
	m := make(map[string]float64, len(ws))
	for _, w := range ws {
		m[w.Strategy] = w.Weight
	}
	return m
}

// CompositeSignal is the meta-strategy's verdict for one candle. It always
// reports both sides' weights so the caller sees how decisive the vote was.
type CompositeSignal struct {
	Side        Side    // chosen direction if Recommended
	LongWeight  float64 // sum of weights of strategies that flagged LONG
	ShortWeight float64 // ditto for SHORT
	NetWeight   float64 // LongWeight − ShortWeight
	Confidence  float64 // |NetWeight| / (LongWeight + ShortWeight); 0..1
	Recommended bool    // true if |NetWeight| crosses threshold
	Triggers    []TaggedSignal
	BestEntry   float64
	BestSL      float64
	BestTP      float64
}

// TaggedSignal pairs a raw strategy signal with the weight that strategy
// carries in this composite vote — useful for debugging which voices won.
type TaggedSignal struct {
	Signal Signal
	Weight float64
}

// ComposeSignal evaluates every strategy on the given candle and folds their
// signals into a weighted vote. If the absolute net weight clears
// threshold, it picks the direction and surfaces the best (highest-weight)
// individual entry/SL/TP triplet from the winning side as the recommended
// execution levels.
func ComposeSignal(candles []Candle, a Analysis, i int, weights map[string]float64, threshold float64) CompositeSignal {
	cs := CompositeSignal{}
	var bestLong, bestShort *TaggedSignal
	for _, strat := range Strategies {
		sig, ok := strat(candles, a, i)
		if !ok {
			continue
		}
		w := weights[sig.Strategy]
		if w <= 0 {
			continue
		}
		ts := TaggedSignal{Signal: sig, Weight: w}
		cs.Triggers = append(cs.Triggers, ts)
		if sig.Side == Long {
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

	if cs.NetWeight >= threshold && bestLong != nil {
		cs.Side = Long
		cs.Recommended = true
		cs.BestEntry = bestLong.Signal.Entry
		cs.BestSL = bestLong.Signal.SL
		cs.BestTP = bestLong.Signal.TP
	} else if -cs.NetWeight >= threshold && bestShort != nil {
		cs.Side = Short
		cs.Recommended = true
		cs.BestEntry = bestShort.Signal.Entry
		cs.BestSL = bestShort.Signal.SL
		cs.BestTP = bestShort.Signal.TP
	}
	return cs
}

// printCompositeSignal writes a CLI-friendly report of the meta-strategy
// verdict for a single candle. If capital > 0, also computes Admirals
// position sizing (1 lot = 100 oz, Tier 1 leverage 1:500).
func printCompositeSignal(symbol, interval string, candle Candle, cs CompositeSignal, runID int64, threshold, capital float64) {
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
	if cs.Side == Short {
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
	fmt.Fprintf(&b, "  %-18s  %-6s  %-8s  %-8s  %-8s  %-8s\n",
		"Strategy", "Trades", "ExpR", "WinRate", "NetR", "Weight")
	for _, w := range cp {
		fmt.Fprintf(&b, "  %-18s  %-6d  %+8.3f  %6.1f%%  %+8.2f  %8.3f\n",
			w.Strategy, w.Trades, w.Expectancy, w.WinRate*100, w.NetR, w.Weight)
	}
	return b.String()
}
