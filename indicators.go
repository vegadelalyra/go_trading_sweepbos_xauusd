package main

import "math"

// Shared price-series indicators used by trend, mean-reversion and breakout
// strategies. All functions return a slice the same length as the input so
// callers can index directly by candle position without offset bookkeeping.
// Leading positions where the window is not yet filled hold 0.
//
// NOTE: EMA lives in bias.go (same package) with the identical SMA-seeded
// contract; we reuse it from here rather than duplicating the symbol.

// RSI implements Wilder's smoothing. The first `period` outputs are 0; the
// seed at index `period` uses the SMA of gains/losses of the first `period`
// price changes, after which we apply Wilder's recursive averaging.
func RSI(closes []float64, period int) []float64 {
	out := make([]float64, len(closes))
	if period <= 0 || len(closes) <= period {
		return out
	}

	// Seed averages from the first `period` price changes.
	var gainSum, lossSum float64
	for i := 1; i <= period; i++ {
		delta := closes[i] - closes[i-1]
		if delta >= 0 {
			gainSum += delta
		} else {
			lossSum -= delta
		}
	}
	avgGain := gainSum / float64(period)
	avgLoss := lossSum / float64(period)
	out[period] = rsiFromAverages(avgGain, avgLoss)

	for i := period + 1; i < len(closes); i++ {
		delta := closes[i] - closes[i-1]
		gain, loss := 0.0, 0.0
		if delta >= 0 {
			gain = delta
		} else {
			loss = -delta
		}
		avgGain = (avgGain*float64(period-1) + gain) / float64(period)
		avgLoss = (avgLoss*float64(period-1) + loss) / float64(period)
		out[i] = rsiFromAverages(avgGain, avgLoss)
	}
	return out
}

func rsiFromAverages(avgGain, avgLoss float64) float64 {
	if avgLoss == 0 {
		if avgGain == 0 {
			return 50
		}
		return 100
	}
	rs := avgGain / avgLoss
	return 100 - 100/(1+rs)
}

// Bollinger returns the SMA mid-line and the upper/lower bands at
// stddevMult population standard deviations. Indices below period-1 hold 0
// on all three series.
func Bollinger(closes []float64, period int, stddevMult float64) (mid, upper, lower []float64) {
	mid = make([]float64, len(closes))
	upper = make([]float64, len(closes))
	lower = make([]float64, len(closes))
	if period <= 0 || len(closes) < period {
		return
	}
	for i := period - 1; i < len(closes); i++ {
		// Window [i-period+1, i].
		var sum float64
		for j := i - period + 1; j <= i; j++ {
			sum += closes[j]
		}
		mean := sum / float64(period)
		var sqSum float64
		for j := i - period + 1; j <= i; j++ {
			d := closes[j] - mean
			sqSum += d * d
		}
		sigma := math.Sqrt(sqSum / float64(period))
		mid[i] = mean
		upper[i] = mean + stddevMult*sigma
		lower[i] = mean - stddevMult*sigma
	}
	return
}
