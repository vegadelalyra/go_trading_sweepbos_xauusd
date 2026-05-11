package main

import (
	"sync"
	"time"
)

type Candle struct {
	Time                   time.Time
	Open, High, Low, Close float64
}

type Side int

const (
	Long Side = iota
	Short
)

func (s Side) String() string {
	if s == Long {
		return "LONG"
	}
	return "SHORT"
}

type Swing struct {
	Index  int
	Price  float64
	IsHigh bool
}

type FVG struct {
	Index   int
	Top     float64
	Bottom  float64
	Bullish bool
	Filled  bool
}

func (f FVG) Mid() float64 { return (f.Top + f.Bottom) / 2 }

type StructureKind int

const (
	StructNone StructureKind = iota
	StructBOSBull
	StructBOSBear
	StructCHoChBull
	StructCHoChBear
)

type StructureEvent struct {
	Index      int
	Kind       StructureKind
	SwingIndex int
	SwingPrice float64
}

type OrderBlock struct {
	Index   int
	Top     float64
	Bottom  float64
	Bullish bool
}

type Analysis struct {
	Swings      []Swing
	FVGs        []FVG
	Structure   []StructureEvent
	OrderBlocks []OrderBlock
}

func Analyze(candles []Candle) Analysis {
	swings := DetectSwings(candles)
	structure := DetectStructure(candles, swings)
	return Analysis{
		Swings:      swings,
		FVGs:        DetectFVGs(candles),
		Structure:   structure,
		OrderBlocks: DetectOrderBlocks(candles, structure),
	}
}

var (
	nyOnce sync.Once
	nyTZ   *time.Location
)

func nyLoc() *time.Location {
	nyOnce.Do(func() {
		l, err := time.LoadLocation("America/New_York")
		if err != nil {
			l = time.FixedZone("EST", -5*3600)
		}
		nyTZ = l
	})
	return nyTZ
}

func toNY(t time.Time) time.Time { return t.In(nyLoc()) }

// 3-candle fractal: swing high if H[i] > H[i-1] && H[i] > H[i+1]; analogous for low.
func DetectSwings(candles []Candle) []Swing {
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

// FVG: candle[i-2].High < candle[i].Low (bull) or candle[i-2].Low > candle[i].High (bear).
// Body of candle[i-1] is the imbalance.
func DetectFVGs(candles []Candle) []FVG {
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

// BOS = body close beyond previous same-direction swing.
// CHoCh = body close beyond the swing that produced the most recent BOS in the opposite direction.
func DetectStructure(candles []Candle, swings []Swing) []StructureEvent {
	out := make([]StructureEvent, 0)
	if len(swings) == 0 {
		return out
	}

	var lastBOSDir int // +1 bull, -1 bear, 0 none

	for i := 0; i < len(candles); i++ {
		body := candleBody(candles[i])

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

		if nearestHigh != nil && body.high > nearestHigh.Price {
			kind := StructBOSBull
			if lastBOSDir == -1 {
				kind = StructCHoChBull
			}
			out = append(out, StructureEvent{Index: i, Kind: kind, SwingIndex: nearestHigh.Index, SwingPrice: nearestHigh.Price})
			lastBOSDir = 1
		}
		if nearestLow != nil && body.low < nearestLow.Price {
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

type bodyRange struct{ low, high float64 }

func candleBody(c Candle) bodyRange {
	if c.Close >= c.Open {
		return bodyRange{c.Open, c.Close}
	}
	return bodyRange{c.Close, c.Open}
}

// On a bullish BOS at index k, the OB is the last bearish candle before the impulsive move.
// On a bearish BOS, the OB is the last bullish candle before the move.
func DetectOrderBlocks(candles []Candle, events []StructureEvent) []OrderBlock {
	out := make([]OrderBlock, 0, len(events))
	for _, e := range events {
		if e.Kind != StructBOSBull && e.Kind != StructBOSBear &&
			e.Kind != StructCHoChBull && e.Kind != StructCHoChBear {
			continue
		}
		bull := e.Kind == StructBOSBull || e.Kind == StructCHoChBull
		// scan backward from e.Index-1 for last opposite-color candle
		for j := e.Index - 1; j >= 0 && j > e.Index-12; j-- {
			c := candles[j]
			if bull && c.Close < c.Open {
				out = append(out, OrderBlock{Index: j, Bottom: c.Low, Top: c.High, Bullish: true})
				break
			}
			if !bull && c.Close > c.Open {
				out = append(out, OrderBlock{Index: j, Bottom: c.Low, Top: c.High, Bullish: false})
				break
			}
		}
	}
	return out
}

