package indicator

import "math"

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
