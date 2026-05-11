package main

import "math"

// SharpeRatio computes the annualized Sharpe ratio from realized R-multiples.
// We use POPULATION stddev (divide by N, not N-1) because rs is the full
// observed sample of trades, not a draw from a larger population — there is
// no inferential gap to correct for.
func SharpeRatio(rs []float64, tradesPerYear float64) float64 {
	if len(rs) < 2 {
		return 0
	}
	var sum float64
	for _, r := range rs {
		sum += r
	}
	mean := sum / float64(len(rs))

	var sq float64
	for _, r := range rs {
		d := r - mean
		sq += d * d
	}
	// Population variance: divide by N. See header comment.
	variance := sq / float64(len(rs))
	std := math.Sqrt(variance)
	if std == 0 {
		return 0
	}
	return mean / std * math.Sqrt(tradesPerYear)
}

// RecoveryFactor = NetR / MaxDD. maxDD is a non-negative number
// (0.2 == 20% drawdown). Handles the zero-DD edge case with signed infinity
// so callers can still order strategies by quality.
func RecoveryFactor(netR, maxDD float64) float64 {
	if maxDD == 0 {
		if netR > 0 {
			return math.Inf(1)
		}
		if netR < 0 {
			return math.Inf(-1)
		}
		return 0
	}
	return netR / maxDD
}

// CalmarRatio = AnnualReturn% / MaxDD%. Same divide-by-zero policy as
// RecoveryFactor — perfect risk-free returns are infinite, perfect flatlines
// are zero, and negative-with-zero-DD shouldn't happen but is handled.
func CalmarRatio(annualReturnPct, maxDDPct float64) float64 {
	if maxDDPct == 0 {
		if annualReturnPct > 0 {
			return math.Inf(1)
		}
		if annualReturnPct < 0 {
			return math.Inf(-1)
		}
		return 0
	}
	return annualReturnPct / maxDDPct
}

// AnnualizedReturn converts a start/end balance over N days into a
// compounded annual percentage. Returns 0 on degenerate input rather than
// NaN/Inf — callers downstream display this directly.
func AnnualizedReturn(startBalance, endBalance float64, daysElapsed int) float64 {
	if daysElapsed <= 0 || startBalance <= 0 {
		return 0
	}
	return (math.Pow(endBalance/startBalance, 365.0/float64(daysElapsed)) - 1) * 100
}

// MaxConsecutiveLosses returns the longest run of strictly-negative R in
// sequence. Zero R (breakeven) breaks the streak.
func MaxConsecutiveLosses(rs []float64) int {
	var best, cur int
	for _, r := range rs {
		if r < 0 {
			cur++
			if cur > best {
				best = cur
			}
		} else {
			cur = 0
		}
	}
	return best
}

// MaxConsecutiveWins mirrors MaxConsecutiveLosses for R > 0.
func MaxConsecutiveWins(rs []float64) int {
	var best, cur int
	for _, r := range rs {
		if r > 0 {
			cur++
			if cur > best {
				best = cur
			}
		} else {
			cur = 0
		}
	}
	return best
}
