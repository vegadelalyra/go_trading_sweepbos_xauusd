package main

import "fmt"

// JudasSwing ──────────────────────────────────────────────────────────────
//
// At the London open the first move is often a fake-out that runs liquidity
// on one side of the Asian range before reversing into the real day's
// direction. We fade that first move: if the London-open candles swept ONE
// side of the Asian range and price has now reclaimed back through the
// midpoint, we take the reversal.

func JudasSwing(candles []Candle, _ Analysis, i int) (Signal, bool) {
	if i < 96 {
		return Signal{}, false
	}
	cur := candles[i]
	ny := toNY(cur.Time)
	h := ny.Hour()
	if h < 3 || h >= 5 {
		return Signal{}, false
	}

	asiaHigh, asiaLow, ok := asianRange(candles, i)
	if !ok {
		return Signal{}, false
	}
	asiaMid := (asiaHigh + asiaLow) / 2

	// Identify "London first hour" candles (from 02:00 EST up to i-1).
	londonStart := i
	for k := i - 1; k >= 0; k-- {
		nyk := toNY(candles[k].Time)
		if nyk.Hour() < 2 || nyk.Hour() >= 5 {
			break
		}
		londonStart = k
	}
	if londonStart >= i {
		return Signal{}, false
	}

	sweptHigh, sweptLow := false, false
	londonHi, londonLo := candles[londonStart].High, candles[londonStart].Low
	for k := londonStart; k < i; k++ {
		if candles[k].High > asiaHigh {
			sweptHigh = true
		}
		if candles[k].Low < asiaLow {
			sweptLow = true
		}
		if candles[k].High > londonHi {
			londonHi = candles[k].High
		}
		if candles[k].Low < londonLo {
			londonLo = candles[k].Low
		}
	}

	// Reversal short: only Asian high was swept, current candle reclaimed below mid.
	if sweptHigh && !sweptLow && cur.Close < asiaMid {
		entry := cur.Close
		sl := londonHi + slBuffer
		tp := tpFromRR(entry, sl, false)
		return buildSignal(
			"JudasSwing", false, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("AsiaH=%.2f LondonHi=%.2f", asiaHigh, londonHi),
		)
	}
	if sweptLow && !sweptHigh && cur.Close > asiaMid {
		entry := cur.Close
		sl := londonLo - slBuffer
		tp := tpFromRR(entry, sl, true)
		return buildSignal(
			"JudasSwing", true, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("AsiaL=%.2f LondonLo=%.2f", asiaLow, londonLo),
		)
	}
	return Signal{}, false
}
