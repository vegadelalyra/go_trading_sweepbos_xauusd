package smc

import "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"

// Analyze runs every primitive detector against the same candle slice and
// returns them in a single Analysis value — strategies can then read whatever
// they need without redoing the per-bar scans.
func Analyze(candles []core.Candle) Analysis {
	swings := DetectSwings(candles)
	structure := DetectStructure(candles, swings)
	fvgs := DetectFVGs(candles)
	return Analysis{
		Swings:      swings,
		FVGs:        fvgs,
		Structure:   structure,
		OrderBlocks: DetectOrderBlocks(candles, structure, swings, fvgs),
	}
}

// DetectSwings returns every 3-candle fractal pivot in chronological order.
// A swing high requires H[i] > H[i-1] AND H[i] > H[i+1]; lows are mirrored.
// Note that swing at index i is only "known" at the close of candle i+1 —
// strategies that use swings at near-realtime should be aware of this 1-bar
// confirmation lag.
func DetectSwings(candles []core.Candle) []Swing {
	out := make([]Swing, 0, len(candles)/3)
	for i := 1; i < len(candles)-1; i++ {
		if candles[i].High > candles[i-1].High && candles[i].High > candles[i+1].High {
			out = append(out, Swing{Index: i, Price: candles[i].High, IsHigh: true})
		}
		if candles[i].Low < candles[i-1].Low && candles[i].Low < candles[i+1].Low {
			out = append(out, Swing{Index: i, Price: candles[i].Low, IsHigh: false})
		}
	}
	return out
}

// DetectFVGs returns every 3-bar imbalance. The middle bar's body is the
// price displacement; the gap region is between the wicks of i-2 and i.
// An FVG at Index=i is confirmed at the close of candle i.
func DetectFVGs(candles []core.Candle) []FVG {
	out := make([]FVG, 0)
	for i := 2; i < len(candles); i++ {
		a := candles[i-2]
		c := candles[i]
		if a.High < c.Low {
			out = append(out, FVG{Index: i, Bottom: a.High, Top: c.Low, Bullish: true})
		} else if a.Low > c.High {
			out = append(out, FVG{Index: i, Bottom: c.High, Top: a.Low, Bullish: false})
		}
	}
	return out
}

// DetectStructure fires BoS / CHoCh events on strict CLOSE beyond the prior
// same- (or opposite-) direction swing — never on body or wick. A wick that
// pierces a swing but pulls back is a Liquidity Sweep, not a structural break.
func DetectStructure(candles []core.Candle, swings []Swing) []StructureEvent {
	out := make([]StructureEvent, 0)
	if len(swings) == 0 {
		return out
	}

	var lastBOSDir int // +1 bull, -1 bear, 0 none

	for i := range candles {
		var nearestHigh, nearestLow *Swing
		for s := range swings {
			sw := &swings[s]
			if sw.Index >= i {
				break
			}
			if sw.IsHigh {
				nearestHigh = sw
			} else {
				nearestLow = sw
			}
		}

		if nearestHigh != nil && candles[i].Close > nearestHigh.Price {
			kind := StructBOSBull
			if lastBOSDir == -1 {
				kind = StructCHoChBull
			}
			out = append(out, StructureEvent{Index: i, Kind: kind, SwingIndex: nearestHigh.Index, SwingPrice: nearestHigh.Price})
			lastBOSDir = 1
		}
		if nearestLow != nil && candles[i].Close < nearestLow.Price {
			kind := StructBOSBear
			if lastBOSDir == 1 {
				kind = StructCHoChBear
			}
			out = append(out, StructureEvent{Index: i, Kind: kind, SwingIndex: nearestLow.Index, SwingPrice: nearestLow.Price})
			lastBOSDir = -1
		}
	}
	return out
}

// DetectOrderBlocks identifies institutional Order Blocks: the last opposite-color
// candle before a structural break (BoS/CHoCh) that also satisfies the three ICT
// validation pillars. Anything that fails any pillar is excluded — the old
// "just grab the last opposite-color body within 12 candles" produced a 60-80%
// false-positive rate that polluted OrderBlockMitigation and BreakerBlock.
//
// Pillar 1 — Prior liquidity sweep: institutional accumulation needs fuel. We
// require that within 10 candles before the OB, price pierced a prior swing
// (the wick of some bar k took out a swing whose own Index < k). For a bullish
// OB we look for a swept LOW; for bearish, a swept HIGH.
//
// Pillar 2 — Displacement (FVG within 3 candles after OB): the move out of the
// OB must be impulsive enough to leave an imbalance. Same-direction FVG only.
//
// Pillar 3 — Mitigation zero: between the OB and the BoS event, no candle body
// closed beyond the OB's 50% mean threshold. If it did, the imbalance is gone.
//
// Lookback widened from 12 → 20 because some institutional OBs form during
// slower accumulation phases that pre-date the impulse.
//
// Each emitted OB records the resolving structure event's index in ResolveIdx,
// so strategies can gate on "OB visible by time i" instead of "OB candle by time i".
func DetectOrderBlocks(candles []core.Candle, events []StructureEvent, swings []Swing, fvgs []FVG) []OrderBlock {
	out := make([]OrderBlock, 0, len(events))
	for _, e := range events {
		if e.Kind != StructBOSBull && e.Kind != StructBOSBear &&
			e.Kind != StructCHoChBull && e.Kind != StructCHoChBear {
			continue
		}
		bull := e.Kind == StructBOSBull || e.Kind == StructCHoChBull

		obIdx := -1
		for j := e.Index - 1; j >= 0 && j > e.Index-20; j-- {
			c := candles[j]
			if bull && c.Close < c.Open {
				obIdx = j
				break
			}
			if !bull && c.Close > c.Open {
				obIdx = j
				break
			}
		}
		if obIdx < 0 {
			continue
		}
		ob := candles[obIdx]

		// Pillar 1: prior-liquidity sweep within the 10 candles before the OB.
		swept := false
		for k := obIdx - 1; k >= 0 && obIdx-k <= 10; k-- {
			for s := len(swings) - 1; s >= 0; s-- {
				sw := swings[s]
				if sw.Index >= k {
					continue
				}
				if k-sw.Index > 30 {
					break
				}
				if bull && !sw.IsHigh && candles[k].Low < sw.Price {
					swept = true
					break
				}
				if !bull && sw.IsHigh && candles[k].High > sw.Price {
					swept = true
					break
				}
			}
			if swept {
				break
			}
		}
		if !swept {
			continue
		}

		// Pillar 2: same-direction FVG within 3 candles after the OB.
		hasFVG := false
		for _, f := range fvgs {
			if f.Index <= obIdx {
				continue
			}
			if f.Index > obIdx+3 {
				break
			}
			if f.Bullish == bull {
				hasFVG = true
				break
			}
		}
		if !hasFVG {
			continue
		}

		// Pillar 3: no body-close beyond OB mean between the OB and the BoS event.
		mid := (ob.High + ob.Low) / 2
		invalidated := false
		for j := obIdx + 1; j < e.Index; j++ {
			if bull && candles[j].Close < mid {
				invalidated = true
				break
			}
			if !bull && candles[j].Close > mid {
				invalidated = true
				break
			}
		}
		if invalidated {
			continue
		}

		out = append(out, OrderBlock{Index: obIdx, ResolveIdx: e.Index, Bottom: ob.Low, Top: ob.High, Bullish: bull})
	}
	return out
}
