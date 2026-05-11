package ta

import (
	"fmt"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/indicator"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// BollingerReversalConfig — mean-reversion tunables.
type BollingerReversalConfig struct {
	BBPeriod      int
	BBStdDev      float64
	RSIPeriod     int
	RSIOversold   float64
	RSIOverbought float64
	SLBuffer      float64
	RR            float64
}

func DefaultBollingerReversalConfig() BollingerReversalConfig {
	return BollingerReversalConfig{
		BBPeriod:      20,
		BBStdDev:      2.0,
		RSIPeriod:     14,
		RSIOversold:   30.0,
		RSIOverbought: 70.0,
		SLBuffer:      strategy.DefaultSLBuffer,
		RR:            strategy.DefaultRR,
	}
}

// BollingerReversal — prior candle pierced a band while RSI was at the
// matching extreme; current candle closes back inside and RSI turns.
// Targets the snap-back that follows over-extension. Best in ranges; trend
// filters elsewhere are expected to gate this when conditions favor
// continuation.
type BollingerReversal struct {
	Cfg BollingerReversalConfig
}

func (s *BollingerReversal) Meta() strategy.Meta {
	return strategy.Meta{
		Name:        "BollingerRev",
		Family:      strategy.FamilyTA,
		Description: "Bollinger pierce + RSI extreme mean-reversion entry",
	}
}

func (s *BollingerReversal) Detect(candles []core.Candle, _ prim.Analysis, i int) (core.Signal, bool) {
	if i < s.Cfg.BBPeriod+s.Cfg.RSIPeriod+1 {
		return core.Signal{}, false
	}

	closes := make([]float64, i+1)
	for k := 0; k <= i; k++ {
		closes[k] = candles[k].Close
	}
	_, upper, lower := indicator.Bollinger(closes, s.Cfg.BBPeriod, s.Cfg.BBStdDev)
	rsi := indicator.RSI(closes, s.Cfg.RSIPeriod)

	prev := candles[i-1]
	cur := candles[i]

	bullSetup := prev.Close < lower[i-1] &&
		cur.Close > prev.Close &&
		rsi[i-1] < s.Cfg.RSIOversold &&
		rsi[i] > rsi[i-1]

	bearSetup := prev.Close > upper[i-1] &&
		cur.Close < prev.Close &&
		rsi[i-1] > s.Cfg.RSIOverbought &&
		rsi[i] < rsi[i-1]

	if !bullSetup && !bearSetup {
		return core.Signal{}, false
	}

	bull := bullSetup
	entry := cur.Close
	var sl float64
	if bull {
		sl = prev.Low - s.Cfg.SLBuffer
	} else {
		sl = prev.High + s.Cfg.SLBuffer
	}
	tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, bull)

	reason := fmt.Sprintf("BB(%dσ) reversal RSI=%.1f", int(s.Cfg.BBStdDev), rsi[i])
	return strategy.BuildSignal("BollingerRev", bull, entry, sl, tp, i, cur.Time, reason)
}
