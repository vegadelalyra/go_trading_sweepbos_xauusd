package main

import "fmt"

// BollingerReversal ────────────────────────────────────────────────────────
//
// Mean-reversion play: prior candle pierced a Bollinger band while RSI was
// at an extreme; current candle closes back inside and RSI turns. The setup
// targets the "snap-back" that follows over-extended moves. SL sits beyond
// the piercing wick. Best in ranging regimes — trend filters elsewhere are
// expected to gate this when conditions favor continuation.

const (
	bbPeriod      = 20
	bbStdDev      = 2.0
	bbRSIPeriod   = 14
	rsiOversold   = 30.0
	rsiOverbought = 70.0
)

func BollingerReversal(candles []Candle, a Analysis, i int) (Signal, bool) {
	if i < bbPeriod+bbRSIPeriod+1 {
		return Signal{}, false
	}

	closes := make([]float64, i+1)
	for k := 0; k <= i; k++ {
		closes[k] = candles[k].Close
	}
	_, upper, lower := Bollinger(closes, bbPeriod, bbStdDev)
	rsi := RSI(closes, bbRSIPeriod)

	prev := candles[i-1]
	cur := candles[i]

	// Long: prev pierced lower, current reentered higher, RSI turning up from oversold.
	bullSetup := prev.Close < lower[i-1] &&
		cur.Close > prev.Close &&
		rsi[i-1] < rsiOversold &&
		rsi[i] > rsi[i-1]

	// Short: prev pierced upper, current reentered lower, RSI turning down from overbought.
	bearSetup := prev.Close > upper[i-1] &&
		cur.Close < prev.Close &&
		rsi[i-1] > rsiOverbought &&
		rsi[i] < rsi[i-1]

	if !bullSetup && !bearSetup {
		return Signal{}, false
	}

	bull := bullSetup
	entry := cur.Close
	var sl float64
	if bull {
		sl = prev.Low - slBuffer
	} else {
		sl = prev.High + slBuffer
	}
	tp := tpFromRR(entry, sl, bull)

	reason := fmt.Sprintf("BB(%dσ) reversal RSI=%.1f", int(bbStdDev), rsi[i])
	return buildSignal("BollingerRev", bull, entry, sl, tp, i, cur.Time, reason)
}
