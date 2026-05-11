package indicator

// EMA computes the exponential moving average over values with the given period.
// The output slice has the same length as values; the first (period-1) entries
// are 0 (not yet computable). values[period-1] is seeded with a Simple Moving
// Average of the first `period` items — this matches the convention used by
// trading platforms (TradingView, MT4/5) and avoids the cold-start bias of a
// pure recursive EMA started from a single sample.
func EMA(values []float64, period int) []float64 {
	out := make([]float64, len(values))
	if period <= 0 || len(values) < period {
		return out
	}
	var sum float64
	for i := 0; i < period; i++ {
		sum += values[i]
	}
	out[period-1] = sum / float64(period)
	k := 2.0 / float64(period+1)
	for i := period; i < len(values); i++ {
		out[i] = (values[i] * k) + (out[i-1] * (1 - k))
	}
	return out
}
