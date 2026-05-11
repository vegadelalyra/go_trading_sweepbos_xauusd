package main

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BootstrapResult holds the resampled distributions for each headline metric.
// *Mean is the average across iterations; *Low95 / *High95 are the 2.5 / 97.5
// percentile cuts — the canonical 95% confidence interval.
type BootstrapResult struct {
	SharpeMean, SharpeLow95, SharpeHigh95 float64
	MaxDDMean, MaxDDLow95, MaxDDHigh95    float64
	CalmarMean, CalmarLow95, CalmarHigh95 float64
	NetRMean, NetRLow95, NetRHigh95       float64
	Iterations                            int
	BlockMean                             float64
}

// StationaryBlockBootstrap resamples blocks of consecutive trades using a
// geometric block-length distribution (Politis-Romano 1994). Geometric
// block-lengths preserve autocorrelation while still producing valid
// pseudo-histories — important because trades from the same regime tend
// to cluster, and an i.i.d. bootstrap underestimates the true tail risk.
//
//	trades        OOS trades (or any chronological trade stream)
//	iterations    ≥5000 recommended for stable tail percentiles
//	meanBlockLen  expected block length (30-50 trades typical)
//	startBalance  base equity for the simulated MaxDD walk
//	riskPct       fraction of equity risked per trade (matches BacktestParams.RiskPct)
func StationaryBlockBootstrap(trades []Trade, iterations int, meanBlockLen, startBalance, riskPct float64) BootstrapResult {
	if len(trades) < 30 || iterations < 100 {
		return BootstrapResult{}
	}
	rng := rand.New(rand.NewSource(42))
	pBlock := 1.0 / meanBlockLen

	sharpes := make([]float64, iterations)
	maxDDs := make([]float64, iterations)
	calmars := make([]float64, iterations)
	netRs := make([]float64, iterations)

	N := len(trades)
	for it := 0; it < iterations; it++ {
		resample := make([]Trade, 0, N)
		for len(resample) < N {
			start := rng.Intn(N)
			for {
				resample = append(resample, trades[start%N])
				if len(resample) >= N {
					break
				}
				start++
				if rng.Float64() < pBlock {
					break
				}
			}
		}
		resample = resample[:N]

		balance := startBalance
		peak := balance
		maxDD := 0.0
		netR := 0.0
		rs := make([]float64, N)
		for i, t := range resample {
			balance *= 1 + (t.R * riskPct)
			netR += t.R
			rs[i] = t.R
			if balance > peak {
				peak = balance
			}
			if peak > 0 {
				if dd := (peak - balance) / peak; dd > maxDD {
					maxDD = dd
				}
			}
		}
		// We don't know the calendar span of a resampled stream, so
		// annualise on 252 (the conventional trading-days denominator)
		// instead of trades-per-year derived from the actual data. Each
		// bootstrap pass therefore uses the same scaling, keeping the
		// CI comparable across iterations.
		sharpes[it] = SharpeRatio(rs, 252)
		maxDDs[it] = maxDD
		netRs[it] = netR
		annRet := (balance/startBalance - 1) * 100
		calmars[it] = CalmarRatio(annRet, maxDD*100)
	}

	br := BootstrapResult{Iterations: iterations, BlockMean: meanBlockLen}
	br.SharpeMean, br.SharpeLow95, br.SharpeHigh95 = quantile95(sharpes)
	br.MaxDDMean, br.MaxDDLow95, br.MaxDDHigh95 = quantile95(maxDDs)
	br.CalmarMean, br.CalmarLow95, br.CalmarHigh95 = quantile95(calmars)
	br.NetRMean, br.NetRLow95, br.NetRHigh95 = quantile95(netRs)
	return br
}

// quantile95 returns (mean, 2.5%, 97.5%) of xs without modifying it.
func quantile95(xs []float64) (mean, low, high float64) {
	if len(xs) == 0 {
		return 0, 0, 0
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	sum := 0.0
	for _, v := range sorted {
		sum += v
	}
	mean = sum / float64(len(sorted))
	loIdx := int(math.Round(float64(len(sorted)-1) * 0.025))
	hiIdx := int(math.Round(float64(len(sorted)-1) * 0.975))
	return mean, sorted[loIdx], sorted[hiIdx]
}

// loadBacktestTrades pulls the realised trades of a stored run into the
// in-memory Trade type. Only the fields the bootstrap needs (R, Win,
// EntryTime, ExitTime) are populated.
func loadBacktestTrades(ctx context.Context, pool *pgxpool.Pool, runID int64) ([]Trade, error) {
	rows, err := pool.Query(ctx, `
		SELECT strategy, side, r_realized, win, entry_time, exit_time
		FROM backtest_trades
		WHERE run_id = $1
		ORDER BY entry_time ASC
	`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Trade
	for rows.Next() {
		var t Trade
		var sideStr string
		if err := rows.Scan(&t.Strategy, &sideStr, &t.R, &t.Win, &t.EntryTime, &t.ExitTime); err != nil {
			return nil, err
		}
		if sideStr == "SHORT" {
			t.Side = Short
		} else {
			t.Side = Long
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// formatBootstrap renders a bootstrap result to the CLI.
func formatBootstrap(br BootstrapResult, n int) string {
	return fmt.Sprintf(
		"\n── Bootstrap (%d iterations, block≈%.0f, N=%d) ───────────────\n"+
			"  Sharpe   mean=%+6.2f   95%% CI [%+6.2f, %+6.2f]\n"+
			"  MaxDD    mean=%5.1f%%  95%% CI [%5.1f%%, %5.1f%%]\n"+
			"  Calmar   mean=%+6.2f   95%% CI [%+6.2f, %+6.2f]\n"+
			"  NetR     mean=%+7.2f  95%% CI [%+7.2f, %+7.2f]\n",
		br.Iterations, br.BlockMean, n,
		br.SharpeMean, br.SharpeLow95, br.SharpeHigh95,
		br.MaxDDMean*100, br.MaxDDLow95*100, br.MaxDDHigh95*100,
		br.CalmarMean, br.CalmarLow95, br.CalmarHigh95,
		br.NetRMean, br.NetRLow95, br.NetRHigh95,
	)
}
