package meta

import (
	"math"
	"sort"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
)

// MinTradesForShrinkage is the per-strategy IS-trade floor below which we
// don't even attempt a data-driven RR estimate. Strategies with fewer
// than this many trades collapse to the global prior — no information is
// safer than 1-2-sample noise. 5 is intentionally small: WFA windows
// already constrain sample sizes, and any larger floor silences too many
// strategies to be useful. The actual shrinkage weight already pulls
// sparse strategies toward the prior; the floor is the hard "no fit at
// all" guard.
const MinTradesForShrinkage = 5

// DefaultGlobalRR is the prior every shrunk estimate pulls toward when
// strategy-specific evidence is weak. 2.0 matches strategy.DefaultRR —
// the seed RR every strategy already ships with — so a sparse strategy
// reverts to its current production behaviour rather than to a synthetic
// number. Callers who want a data-derived prior (e.g. median across
// strategies) compute one and pass it explicitly to BayesianShrinkRR.
const DefaultGlobalRR = 2.0

// BayesianShrinkRR computes per-strategy shrunk RR estimates from an IS
// trade slice. The formula is the classic empirical-Bayes James-Stein
// shrinkage:
//
//	θ_i_shrink = w_i × θ_i_MLE + (1 - w_i) × θ_global
//	w_i        = n_i / (n_i + σ²_intra / σ²_inter)
//
// where θ_i_MLE is the mean R of strategy i's WINNING trades (the
// "asymmetry" the strategy actually achieved), n_i is the number of
// winning trades, σ²_intra is the average within-strategy variance of
// winning-trade R-multiples (signal noise) and σ²_inter is the
// across-strategy variance of θ_i_MLE around θ_global (between-strategy
// dispersion).
//
// Interpretation:
//   - High σ²_intra / σ²_inter → low signal-to-noise → small w_i → most
//     strategies pull toward θ_global. The right answer when individual
//     strategies are noisy and don't differ much from each other.
//   - Large n_i → w_i → 1 → strategy's own MLE dominates. A high-volume
//     strategy with stable wins gets to keep its own RR.
//
// Why only WINNING trades inform the MLE: losses are by definition -1R
// (the SL distance is the unit), so loss-trade R values carry no
// information about how rewarding the TP side is. The "RR" we're
// shrinking is the asymmetry — what did the strategy collect when it
// was right — not the realised expectancy.
//
// Defensive corners:
//   - n_i < MinTradesForShrinkage OR strategy has zero winning trades →
//     θ_shrink = globalRR (no information, use prior).
//   - σ²_inter ≤ 0 (every strategy has the exact same θ_MLE, degenerate
//     case) → w_i = 0 for every strategy, all collapse to globalRR.
//   - σ²_intra ≤ 0 (every win contributes the same R, perfectly stable
//     strategies) → w_i = 1, the MLE wins outright.
//   - globalRR ≤ 0 → caller forgot to supply a prior, fall back to
//     DefaultGlobalRR so the math never produces a nonsense TP.
//
// Output is a name → shrunk-RR map; strategies that yielded no usable
// information (no wins, too few trades) appear with value globalRR.
// Callers who want to fold this into engine.BacktestParams should pass
// the result as-is; the engine treats any positive value as an override
// of the strategy's intrinsic RR.
func BayesianShrinkRR(trades []core.Trade, globalRR float64) map[string]float64 {
	if globalRR <= 0 {
		globalRR = DefaultGlobalRR
	}

	// Group winning R-multiples by strategy. Losses contribute nothing
	// to the asymmetry estimate (they are -1R by construction).
	winsByStrat := map[string][]float64{}
	allStrats := map[string]int{} // total trades per strategy (for the floor check)
	for _, t := range trades {
		allStrats[t.Strategy]++
		if t.Win && t.R > 0 {
			winsByStrat[t.Strategy] = append(winsByStrat[t.Strategy], t.R)
		}
	}

	// Per-strategy MLE + within-strategy variance of winning Rs.
	type stratStat struct {
		nWins    int
		nTotal   int
		mleRR    float64
		intraVar float64
	}
	stats := map[string]stratStat{}
	for name, total := range allStrats {
		wins := winsByStrat[name]
		st := stratStat{nTotal: total, nWins: len(wins)}
		if len(wins) == 0 {
			stats[name] = st
			continue
		}
		var sum float64
		for _, r := range wins {
			sum += r
		}
		st.mleRR = sum / float64(len(wins))
		// Population variance — same convention as the rest of the codebase
		// (computeWeights, metrics.SharpeRatio, STDDEV_POP).
		var sq float64
		for _, r := range wins {
			d := r - st.mleRR
			sq += d * d
		}
		st.intraVar = sq / float64(len(wins))
		stats[name] = st
	}

	// σ²_intra: average of per-strategy within-strategy variances, taken
	// over strategies that have at least one win. Strategies with no wins
	// can't contribute a variance, so they don't participate in the mean.
	var intraSum float64
	var intraN int
	for _, s := range stats {
		if s.nWins > 0 {
			intraSum += s.intraVar
			intraN++
		}
	}
	sigmaIntra := 0.0
	if intraN > 0 {
		sigmaIntra = intraSum / float64(intraN)
	}

	// σ²_inter: variance of θ_i_MLE across strategies that have at least
	// one win, around the GLOBAL prior (not the sample mean) — using the
	// prior keeps the shrinkage anchored even when most strategies
	// systematically over- or under-shoot, which matches the Bayesian
	// interpretation (the prior is fixed, the data is what varies around it).
	var interSum float64
	for _, s := range stats {
		if s.nWins > 0 {
			d := s.mleRR - globalRR
			interSum += d * d
		}
	}
	sigmaInter := 0.0
	if intraN > 0 {
		sigmaInter = interSum / float64(intraN)
	}

	out := make(map[string]float64, len(stats))
	for name, s := range stats {
		// No information path: collapse to the prior. The MinTradesForShrinkage
		// floor uses TOTAL trades because a strategy that had 100 trades
		// but only 1 win is still extremely noisy on the asymmetry side —
		// we want both "some history" and "some wins" before fitting.
		if s.nTotal < MinTradesForShrinkage || s.nWins == 0 {
			out[name] = globalRR
			continue
		}
		// Degenerate σ²_inter (every strategy agrees with the prior to
		// numerical precision): w_i = 0, everyone pulls to globalRR.
		if sigmaInter <= 1e-12 {
			out[name] = globalRR
			continue
		}
		// Degenerate σ²_intra (perfectly stable winning Rs within
		// strategies): w_i = 1, the MLE wins. Rare but well-defined.
		if sigmaIntra <= 1e-12 {
			out[name] = s.mleRR
			continue
		}
		ratio := sigmaIntra / sigmaInter
		w := float64(s.nWins) / (float64(s.nWins) + ratio)
		shrunk := w*s.mleRR + (1-w)*globalRR
		// Floor at a tiny positive number so the engine override never
		// produces a nonsense TP (TPFromRR uses rr<=0 to fall back to
		// DefaultRR; we don't want to bypass our intentional zero check).
		if shrunk < 0 {
			shrunk = globalRR
		}
		out[name] = shrunk
	}
	return out
}

// MedianRRPrior computes a robust global RR prior from an IS trade
// slice — the median of per-strategy mean-winning-R values, ignoring
// strategies with zero wins. Use this when the caller wants the prior
// to track the data rather than be pinned at strategy.DefaultRR.
//
// Returns DefaultGlobalRR when no strategy has any wins (degenerate
// dataset — caller should probably not be running shrinkage anyway,
// but the function stays well-defined).
func MedianRRPrior(trades []core.Trade) float64 {
	winsByStrat := map[string][]float64{}
	for _, t := range trades {
		if t.Win && t.R > 0 {
			winsByStrat[t.Strategy] = append(winsByStrat[t.Strategy], t.R)
		}
	}
	if len(winsByStrat) == 0 {
		return DefaultGlobalRR
	}
	means := make([]float64, 0, len(winsByStrat))
	for _, ws := range winsByStrat {
		var sum float64
		for _, r := range ws {
			sum += r
		}
		means = append(means, sum/float64(len(ws)))
	}
	sort.Float64s(means)
	n := len(means)
	if n%2 == 1 {
		return means[n/2]
	}
	med := (means[n/2-1] + means[n/2]) / 2
	if med <= 0 || math.IsNaN(med) {
		return DefaultGlobalRR
	}
	return med
}
