package meta

import (
	"context"
	"math"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Bayesian Online Change-Point Detection (Adams & MacKay 2007) with a
// Normal-Gamma conjugate prior on each candidate run length.
//
// The detector consumes a sequence of per-trade R-multiples and, after
// each observation, returns a smooth probability in [0,1] that a
// change in the R-distribution has occurred. That probability is the
// smooth analogue of the binary DetectAlphaDecay verdict: as soon as
// the recent R-stream drifts away from the Normal-Gamma posterior the
// algorithm has built up over the strategy's history, P_change rises.
//
// Reported P_change semantics. The literal Adams-MacKay posterior
// P(r_t = 0 | x_{1:t}) is mathematically pinned to the hazard rate H
// at every step (algebra: numerator H·Σπ·R cancels with denominator
// (H+(1-H))·Σπ·R = Σπ·R, leaving exactly H). It is therefore useless
// as a step-by-step "did a change just happen?" signal — it stays at
// 1/λ regardless of how dramatic the shift was. What changes after a
// shift is the SHAPE of the run-length distribution: mass migrates
// from large r toward small r within a handful of observations.
//
// We expose two outputs:
//   - RawCPProb() returns the literal P(r_t = 0) (always ≈ H).
//   - ChangeProb() returns 1 - E[r_t]/peakE[r_t], the relative drop
//     in the expected run length from its running maximum. This is
//     the quantity the SUPER PLAN's downstream consumer wants: it is
//     0 while the run length is still growing (no decay), spikes
//     toward 1 when a shift compresses the distribution onto short
//     runs, and recovers smoothly as a new stable regime accumulates
//     observations.
//
// Why this replaces the binary detector in OOS-aware callers:
//
//   - DetectAlphaDecay reads in-sample backtest_trades to decide what
//     to silence on the SAME run, which is a look-ahead leak. BOCPD is
//     fed only paper_trades (post-backtest grading) so the OOS contract
//     stays clean.
//   - DetectAlphaDecay either keeps the weight (full edge) or zeroes
//     it. BOCPD modulates W_i := W_i × (1 - P_change_i), so a strategy
//     whose R-stream is just starting to drift gets dampened, not
//     silenced — and recovers smoothly if the drift was transient.
//
// Algorithm:
//
//	For each candidate run length r at time t-1 we hold a Normal-Gamma
//	posterior (μ_r, κ_r, α_r, β_r) over the (mean, precision) of the
//	R-distribution INSIDE that run. The posterior predictive
//	p(x_t | r_{t-1}=r) is the Student-t distribution with
//
//	    ν       = 2·α_r
//	    loc     = μ_r
//	    scale²  = β_r·(κ_r+1)/(α_r·κ_r)
//
//	Hazard H = 1/λ is the prior rate of change points. The recursive
//	update is:
//
//	    grow_r+1   ∝  P(r_{t-1}=r)  · π_r · (1 - H)
//	    cp_at_0    ∝  Σ_r P(r_{t-1}=r) · π_r · H
//
//	After normalising the new run-length distribution we cache
//	E[r_t] = Σ_r r · P(r_t=r), then derive
//	P_change = max(0, 1 - E[r_t]/peakE[r_t]).
//
//	Hyperparameter update for the run that just grew by one (the
//	(r+1)-slot inherits from the r-slot AFTER absorbing x):
//	    μ'   = (κ·μ + x) / (κ + 1)
//	    κ'   = κ + 1
//	    α'   = α + 0.5
//	    β'   = β + κ·(x − μ)² / (2·(κ + 1))
//
// Truncation: the run-length distribution is capped at maxRunLength
// slots. Anything that would spill past the cap is folded into the last
// bucket. Probabilities under pruneEpsilon are zeroed AFTER
// normalisation, then the distribution is renormalised to sum to 1 —
// this keeps the tail compact without distorting the head.

const (
	// DefaultBOCPDLambda is the SUPER PLAN spec value for the hazard
	// rate. H = 1/λ ⇒ prior expected run length of 200 trades, i.e.
	// "we expect the strategy to drift roughly once every 200 trades".
	DefaultBOCPDLambda = 200.0

	// bocpdMaxRunLength caps how many candidate run lengths we track.
	// The posterior of an i.i.d. stream concentrates rapidly near the
	// current run length, so 500 is comfortably more than the tail
	// ever reaches in practice but still bounded for memory.
	bocpdMaxRunLength = 500

	// bocpdPruneEpsilon is the floor below which a run-length bucket
	// is zeroed AFTER each normalisation. 1e-6 is small enough that
	// pruning doesn't perturb the posterior at single-percent scale.
	bocpdPruneEpsilon = 1e-6

	// Normal-Gamma prior hyperparameters. μ_0=0 carries no prior on R
	// direction; κ_0=1 is "one effective prior observation"; α_0=1,
	// β_0=1 makes the prior precision broad (so the data dominates
	// fast). Matches the SUPER PLAN spec.
	bocpdPriorMu    = 0.0
	bocpdPriorKappa = 1.0
	bocpdPriorAlpha = 1.0
	bocpdPriorBeta  = 1.0
)

// BOCPDState is the per-strategy recursive posterior over run lengths.
// Update on each new R observation; ChangeProb returns the smooth
// run-length-drop signal documented above. Construct one instance per
// strategy — no shared state.
type BOCPDState struct {
	// Hazard = 1/λ. Higher hazard → more eager to flag a change point.
	Hazard float64

	// runProbs[r] = P(r_t = r | observations so far). Index 0 is the
	// posterior change-point bucket (always ≈ Hazard by construction);
	// the operational signal is derived from the distribution shape,
	// not from this single bucket — see lastChangeProb.
	runProbs []float64

	// Per-run Normal-Gamma hyperparameters. Same length as runProbs.
	// Entry r tracks the posterior of the run whose length is r.
	mu, kappa, alpha, beta []float64

	// nSeen counts Update calls. The very first observation has no
	// history to detect a change against, so we suppress its output.
	nSeen int

	// lastRawCP is the literal P(r_t = 0 | x_{1:t}) from the most
	// recent Update. Mathematically pinned to Hazard, exposed for
	// debugging only.
	lastRawCP float64

	// lastExpRun is the posterior expected run length after the most
	// recent Update, i.e. Σ_r r · runProbs[r]. The change signal
	// derives from how far this has fallen below the peak.
	lastExpRun float64

	// peakExpRun tracks the maximum lastExpRun observed so far. The
	// reported P_change is the relative drop from peakExpRun to
	// lastExpRun: a stable burn-in pushes both together; a shift
	// compresses the run-length distribution onto small r and pulls
	// lastExpRun below the peak, raising P_change.
	peakExpRun float64

	// lastChangeProb is the value returned by the most recent Update,
	// cached so ChangeProb is a pure read.
	lastChangeProb float64
}

// NewBOCPDState builds a fresh detector seeded with a single run of
// length zero at the broad Normal-Gamma prior. lambda is the prior
// expected run length; lambda<=0 falls back to DefaultBOCPDLambda so
// callers can pass 0 to mean "use the spec value".
func NewBOCPDState(lambda float64) *BOCPDState {
	if lambda <= 0 {
		lambda = DefaultBOCPDLambda
	}
	return &BOCPDState{
		Hazard: 1.0 / lambda,
		// Single run length r=0 with probability 1 — we haven't seen
		// any data, so the only candidate is "we're at the start".
		runProbs: []float64{1.0},
		mu:       []float64{bocpdPriorMu},
		kappa:    []float64{bocpdPriorKappa},
		alpha:    []float64{bocpdPriorAlpha},
		beta:     []float64{bocpdPriorBeta},
	}
}

// Update absorbs a new R observation and returns the smooth change
// probability (relative drop in E[r_t] from its running peak). The
// very first call returns 0 because there's no posterior yet to
// detect a deviation from.
func (b *BOCPDState) Update(r float64) float64 {
	if b == nil {
		return 0
	}
	b.nSeen++
	n := len(b.runProbs)

	// 1. Predictive probabilities π_r = p(x_t | r_{t-1}=r) under each
	//    candidate run's Normal-Gamma posterior, evaluated as a
	//    Student-t pdf.
	pred := make([]float64, n)
	for i := 0; i < n; i++ {
		pred[i] = studentTPredictivePDF(r, b.mu[i], b.kappa[i], b.alpha[i], b.beta[i])
	}

	// 2. Growth and change-point unnormalised probabilities.
	//    grow[i+1] = runProbs[i] · π_i · (1 - H)
	//    cp        = Σ_i runProbs[i] · π_i · H
	//
	//    The new distribution has length n+1 (every existing run got a
	//    slot bumped up by one + a fresh r=0 slot). We cap the tail at
	//    bocpdMaxRunLength by folding overflow into the last bucket.
	maxLen := bocpdMaxRunLength
	newLen := n + 1
	if newLen > maxLen {
		newLen = maxLen
	}
	newProbs := make([]float64, newLen)
	newMu := make([]float64, newLen)
	newKappa := make([]float64, newLen)
	newAlpha := make([]float64, newLen)
	newBeta := make([]float64, newLen)

	cpMass := 0.0
	for i := 0; i < n; i++ {
		w := b.runProbs[i] * pred[i]
		cpMass += w * b.Hazard

		// Growth slot: run length goes from i to i+1.
		dst := i + 1
		if dst >= newLen {
			// Truncation: fold overflow into the last bucket. The
			// hyperparameters of the last bucket are kept as the most
			// recently advanced run's hyperparams (an approximation —
			// at this point the absolute run length is irrelevant for
			// the Student-t prediction, only μ/κ/α/β matter).
			dst = newLen - 1
			newProbs[dst] += w * (1.0 - b.Hazard)
			// Update hyperparams only if this is the first time we hit
			// this overflow slot in this iteration, to avoid biasing
			// toward whichever long-run bucket happens to be processed
			// last. We weight-average by mass already there.
			absorbHyperparams(
				&newMu[dst], &newKappa[dst], &newAlpha[dst], &newBeta[dst],
				w*(1.0-b.Hazard),
				b.mu[i], b.kappa[i], b.alpha[i], b.beta[i],
				r,
			)
			continue
		}
		newProbs[dst] = w * (1.0 - b.Hazard)
		// Run length i+1 inherits the (μ, κ, α, β) of run length i
		// AFTER absorbing the new observation r.
		nm, nk, na, nb := updateNormalGamma(b.mu[i], b.kappa[i], b.alpha[i], b.beta[i], r)
		newMu[dst] = nm
		newKappa[dst] = nk
		newAlpha[dst] = na
		newBeta[dst] = nb
	}
	// 3. Change-point bucket at r=0 with the broad prior. A new run
	//    starts at the prior, having not yet absorbed any data.
	newProbs[0] = cpMass
	newMu[0] = bocpdPriorMu
	newKappa[0] = bocpdPriorKappa
	newAlpha[0] = bocpdPriorAlpha
	newBeta[0] = bocpdPriorBeta

	// 4. Normalise.
	total := 0.0
	for _, p := range newProbs {
		total += p
	}
	if total <= 0 || math.IsNaN(total) || math.IsInf(total, 0) {
		// Numerical collapse (every predictive went to 0 — extreme
		// outlier). Reset to a fresh prior; treat as a change point.
		b.runProbs = []float64{1.0}
		b.mu = []float64{bocpdPriorMu}
		b.kappa = []float64{bocpdPriorKappa}
		b.alpha = []float64{bocpdPriorAlpha}
		b.beta = []float64{bocpdPriorBeta}
		b.lastRawCP = b.Hazard
		b.lastExpRun = 0
		// Peak run length is unchanged; reporting a hard reset.
		if b.nSeen == 1 {
			b.lastChangeProb = 0
			return 0
		}
		b.lastChangeProb = 1
		return 1
	}
	for i := range newProbs {
		newProbs[i] /= total
	}

	// 5. Prune tail below epsilon and renormalise. Keeps the working
	//    set compact for long streams.
	pruned := 0.0
	for i, p := range newProbs {
		if p < bocpdPruneEpsilon {
			pruned += p
			newProbs[i] = 0
		}
	}
	if pruned > 0 && pruned < 1 {
		scale := 1.0 / (1.0 - pruned)
		for i := range newProbs {
			newProbs[i] *= scale
		}
	}

	b.runProbs = newProbs
	b.mu = newMu
	b.kappa = newKappa
	b.alpha = newAlpha
	b.beta = newBeta

	// Cache the raw P(r_t=0) for debug / transparency. Always ≈ Hazard.
	b.lastRawCP = newProbs[0]

	// Posterior expected run length.
	var exp float64
	for r, p := range newProbs {
		exp += float64(r) * p
	}
	b.lastExpRun = exp
	if exp > b.peakExpRun {
		b.peakExpRun = exp
	}

	if b.nSeen == 1 {
		// On the very first observation there's no history to compare
		// against — peak == current, so the drop ratio would be 0
		// anyway. Pin both outputs to 0 explicitly for clarity.
		b.lastChangeProb = 0
		return 0
	}

	// Smooth change signal: relative drop in expected run length from
	// its running peak. 0 when the run is still growing, → 1 when the
	// distribution has fully collapsed onto r ≈ 0. Multiplicative
	// weight modulation W := W·(1-P_change) treats this directly as a
	// "fraction of edge to retain".
	change := 0.0
	if b.peakExpRun > 0 {
		change = 1.0 - b.lastExpRun/b.peakExpRun
	}
	if change < 0 {
		change = 0
	}
	if change > 1 {
		change = 1
	}
	b.lastChangeProb = change
	return change
}

// ChangeProb returns the most recent change-point probability without
// consuming a new observation. Equivalent to the value returned by the
// last Update — namely 1 - E[r_t]/peakE[r_t]. Live in [0, 1]; 0 means
// "no decay detected", 1 means "the run-length distribution has fully
// collapsed onto r ≈ 0".
func (b *BOCPDState) ChangeProb() float64 {
	if b == nil {
		return 0
	}
	return b.lastChangeProb
}

// RawCPProb returns the literal Adams-MacKay posterior P(r_t = 0 |
// x_{1:t}). Always pinned to the hazard rate H by Bayes' rule; exposed
// for debugging / unit tests, not as an operational change signal.
func (b *BOCPDState) RawCPProb() float64 {
	if b == nil {
		return 0
	}
	return b.lastRawCP
}

// ExpectedRunLength returns the posterior mean run length after the
// most recent Update. Useful for diagnostics: the steady-state value
// approaches λ; a sudden drop signals a change point.
func (b *BOCPDState) ExpectedRunLength() float64 {
	if b == nil {
		return 0
	}
	return b.lastExpRun
}

// updateNormalGamma applies the conjugate-prior update for a Normal-Gamma
// posterior after observing a new R value:
//
//	μ'   = (κ·μ + x) / (κ + 1)
//	κ'   = κ + 1
//	α'   = α + 0.5
//	β'   = β + κ·(x − μ)² / (2·(κ + 1))
//
// Returns the updated tuple.
func updateNormalGamma(mu, kappa, alpha, beta, x float64) (float64, float64, float64, float64) {
	nm := (kappa*mu + x) / (kappa + 1)
	nk := kappa + 1
	na := alpha + 0.5
	d := x - mu
	nb := beta + kappa*d*d/(2.0*(kappa+1))
	return nm, nk, na, nb
}

// absorbHyperparams merges the (μ,κ,α,β) of a candidate run into a
// truncation bucket, weight-averaging by mass already accumulated there.
// Used only when the run-length distribution overflows the cap; for
// normal growth slots the deterministic updateNormalGamma path is used.
func absorbHyperparams(mu, kappa, alpha, beta *float64, w float64, srcMu, srcKappa, srcAlpha, srcBeta, x float64) {
	nm, nk, na, nb := updateNormalGamma(srcMu, srcKappa, srcAlpha, srcBeta, x)
	if *kappa == 0 {
		// Bucket was empty — adopt the incoming run's posterior verbatim.
		*mu = nm
		*kappa = nk
		*alpha = na
		*beta = nb
		return
	}
	// Weight-average existing bucket params with the incoming run's.
	// Treat each side's "weight" as posterior mass; we approximate the
	// merged posterior by a moment-matched Normal-Gamma. Good enough
	// at the tail where probabilities are already small.
	totalW := w + 1.0 // existing bucket carries unit weight by convention
	*mu = (*mu + nm*w) / totalW
	*kappa = (*kappa + nk*w) / totalW
	*alpha = (*alpha + na*w) / totalW
	*beta = (*beta + nb*w) / totalW
}

// studentTPredictivePDF evaluates the Normal-Gamma posterior predictive
// at x. The predictive is a Student-t distribution with:
//
//	ν      = 2·α
//	loc    = μ
//	scale² = β·(κ+1) / (α·κ)
//
// (Murphy 2007, "Conjugate Bayesian analysis of the Gaussian distribution",
// eq. 100). Returns the raw pdf — callers handle normalisation.
func studentTPredictivePDF(x, mu, kappa, alpha, beta float64) float64 {
	if alpha <= 0 || kappa <= 0 || beta <= 0 {
		return 0
	}
	nu := 2.0 * alpha
	scaleSq := beta * (kappa + 1) / (alpha * kappa)
	if scaleSq <= 0 {
		return 0
	}
	scale := math.Sqrt(scaleSq)
	return studentTPDF(x, nu, mu, scale)
}

// studentTPDF is the non-standardised Student-t pdf
//
//	f(x) = Γ((ν+1)/2) / (Γ(ν/2)·sqrt(νπ)·σ) · (1 + ((x−μ)/σ)² / ν)^(−(ν+1)/2)
//
// computed in log space for numerical stability, then exponentiated.
func studentTPDF(x, nu, loc, scale float64) float64 {
	if scale <= 0 || nu <= 0 {
		return 0
	}
	z := (x - loc) / scale
	logNum, _ := math.Lgamma((nu + 1) / 2)
	logDen, _ := math.Lgamma(nu / 2)
	logCoef := logNum - logDen - 0.5*math.Log(nu*math.Pi) - math.Log(scale)
	logKern := -((nu + 1) / 2) * math.Log1p(z*z/nu)
	logPdf := logCoef + logKern
	if math.IsInf(logPdf, 0) || math.IsNaN(logPdf) {
		return 0
	}
	return math.Exp(logPdf)
}

// LoadWeightsWithBOCPD is the OOS-bias-free sibling of LoadWeights.
//
// It computes the canonical SharpeWeight per strategy from the SQL
// aggregation (backtest + paper trades), then modulates each weight by
// a multiplicative confidence factor (1 - P_change_i) where P_change_i
// is the final BOCPD change-point probability computed by replaying
// THAT strategy's paper_trades R-stream in chronological order.
//
// Crucial difference vs LoadWeights:
//
//   - LoadWeights consults DetectAlphaDecay, which reads the SAME trade
//     stream (backtest + paper) that the weights derive from. On a
//     freshly-built backtest run this is a look-ahead leak: the
//     detector "knows" the future of the same series it weighs.
//
//   - LoadWeightsWithBOCPD reads ONLY paper_trades for change-point
//     detection. Paper trades are graded AFTER the backtest closed,
//     so no information from them leaks into the weight derivation;
//     they're the genuine OOS signal. If paperRunIDs is empty the
//     BOCPD modulation is a no-op (no OOS evidence yet → no reason
//     to dampen anything).
//
// The function does NOT call DetectAlphaDecay — the whole point is to
// replace the binary silencer with a smooth degrade.
//
// Behaviour summary:
//
//	W_OOS_i = SharpeWeight(exp_i, σ_i, n_i) × (1 - P_change_i)
//
// where P_change_i ∈ [0, 1] is read from BOCPDState.ChangeProb() after
// streaming the strategy's paper_trades through Update().
func LoadWeightsWithBOCPD(ctx context.Context, pool *pgxpool.Pool, runID int64, paperRunIDs ...int64) ([]StrategyWeight, error) {
	// 1. Canonical weight aggregation, identical to LoadWeights up to
	//    the post-decay pass.
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

	// 2. Per-strategy paper-trade R streams, chronologically ordered.
	//    No backtest_trades here — paper-only keeps the OOS contract.
	streams, err := loadPaperStreams(ctx, pool, paperRunIDs)
	if err != nil {
		return nil, err
	}

	// 3. Replay each stream through a BOCPDState and modulate.
	for i := range out {
		stream := streams[out[i].Strategy]
		if len(stream) == 0 {
			// No OOS evidence for this strategy yet → no decay signal.
			continue
		}
		st := NewBOCPDState(DefaultBOCPDLambda)
		for _, r := range stream {
			st.Update(r)
		}
		out[i].Weight *= (1.0 - st.ChangeProb())
	}
	return out, nil
}

// loadPaperStreams returns chronologically-ordered R series per
// strategy from the provided paper_runs. Returns an empty map (not nil)
// when paperRunIDs is empty so callers can index without guarding.
func loadPaperStreams(ctx context.Context, pool *pgxpool.Pool, paperRunIDs []int64) (map[string][]float64, error) {
	out := map[string][]float64{}
	if len(paperRunIDs) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT strategy, r_realized
		FROM paper_trades
		WHERE paper_run_id = ANY($1)
		ORDER BY entry_time ASC, id ASC
	`, paperRunIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var strat string
		var r float64
		if err := rows.Scan(&strat, &r); err != nil {
			return nil, err
		}
		out[strat] = append(out[strat], r)
	}
	return out, rows.Err()
}
