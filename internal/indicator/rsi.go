package indicator

// RSI implements Wilder's smoothing. The first `period` outputs are 0; the
// seed at index `period` uses the SMA of gains/losses of the first `period`
// price changes, after which we apply Wilder's recursive averaging.
func RSI(closes []float64, period int) []float64 {
	out := make([]float64, len(closes))
	if period <= 0 || len(closes) <= period {
		return out
	}

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
