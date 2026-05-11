package main

import "fmt"

// OTEFibonacci (Optimal Trade Entry) ──────────────────────────────────────
//
// After a BoS confirms a new directional leg, ICT teaches entering on the
// 62–79% Fibonacci retracement of that leg. This is the "premium / discount"
// zone where smart money typically reloads before continuation.
//
// Definition of the leg:
//   • Leg start: the swing point that was broken (price BEFORE the BoS).
//   • Leg end:   the impulsive extension reached between the BoS and now.
// Entry: midpoint of the 62–79% OTE zone.
// SL:    just beyond the leg start.

func OTEFibonacci(candles []Candle, a Analysis, i int) (Signal, bool) {
	if i < 30 {
		return Signal{}, false
	}
	cur := candles[i]

	// Find the most recent BoS in the last 30 candles.
	var bos *StructureEvent
	for k := len(a.Structure) - 1; k >= 0; k-- {
		ev := a.Structure[k]
		if ev.Index >= i {
			continue
		}
		if i-ev.Index > 30 {
			break
		}
		if ev.Kind == StructBOSBull || ev.Kind == StructBOSBear {
			bos = &a.Structure[k]
			break
		}
	}
	if bos == nil {
		return Signal{}, false
	}
	bull := bos.Kind == StructBOSBull

	// Leg end = the most extreme price from BoS to now in the BoS direction.
	var legEnd float64
	if bull {
		legEnd = candles[bos.Index].High
		for k := bos.Index; k <= i && k < len(candles); k++ {
			if candles[k].High > legEnd {
				legEnd = candles[k].High
			}
		}
	} else {
		legEnd = candles[bos.Index].Low
		for k := bos.Index; k <= i && k < len(candles); k++ {
			if candles[k].Low < legEnd {
				legEnd = candles[k].Low
			}
		}
	}

	// Leg start = swing that was broken (held on the BoS event).
	legStart := bos.SwingPrice
	legSize := legEnd - legStart
	if (bull && legSize <= 0) || (!bull && legSize >= 0) {
		return Signal{}, false
	}

	// 62–79% retracement window.
	fib62 := legEnd - legSize*0.62
	fib79 := legEnd - legSize*0.79
	oteLo, oteHi := fib62, fib79
	if oteLo > oteHi {
		oteLo, oteHi = oteHi, oteLo
	}

	// Is the current candle touching the OTE zone?
	if cur.Low > oteHi || cur.High < oteLo {
		return Signal{}, false
	}

	entry := (oteLo + oteHi) / 2
	var sl float64
	if bull {
		sl = legStart - slBuffer
	} else {
		sl = legStart + slBuffer
	}
	tp := tpFromRR(entry, sl, bull)
	return buildSignal(
		"OTEFibonacci", bull, entry, sl, tp, i, cur.Time,
		fmt.Sprintf("BoS@%d ote=%.2f-%.2f", bos.Index, oteLo, oteHi),
	)
}
