// Package smc encapsulates Smart-Money / ICT primitives — the structural
// observations every SMC strategy reasons about (swings, FVGs, BoS/CHoCh
// events, Order Blocks) plus the single Analyze entry point that produces
// them all in one pass.
//
// The package is intentionally separate from individual strategies so that
// adding a new SMC-flavoured strategy never requires editing this layer,
// and changing a primitive's detection rules cannot accidentally pull a
// strategy along for the ride.
package smc

// Swing is a 3-candle fractal pivot. Index identifies the candle that holds
// the extreme; IsHigh==true means it's a swing high, otherwise a swing low.
type Swing struct {
	Index  int
	Price  float64
	IsHigh bool
}

// FVG (Fair Value Gap) is the 3-candle imbalance pattern:
//
//	candles[i-2].High < candles[i].Low (bullish FVG between i-2 and i, body of i-1)
//	candles[i-2].Low  > candles[i].High (bearish FVG, mirrored)
//
// Index is the third candle — i.e. the bar that confirmed the imbalance.
type FVG struct {
	Index   int
	Top     float64
	Bottom  float64
	Bullish bool
	Filled  bool
}

// Mid returns the FVG midpoint, used by limit-style entries (FVGFill, SilverBullet).
func (f FVG) Mid() float64 { return (f.Top + f.Bottom) / 2 }

// StructureKind enumerates the four order-flow events Analyze produces.
// BoS continues an existing trend; CHoCh marks an inversion of intent.
type StructureKind int

const (
	StructNone StructureKind = iota
	StructBOSBull
	StructBOSBear
	StructCHoChBull
	StructCHoChBear
)

// StructureEvent is a single BoS/CHoCh confirmation: at candle Index the
// Close crossed the previous swing whose own data is referenced by
// SwingIndex / SwingPrice.
type StructureEvent struct {
	Index      int
	Kind       StructureKind
	SwingIndex int
	SwingPrice float64
}

// OrderBlock carries two distinct timestamps:
//   - Index      → the OB candle itself (the opposite-color body preceding the impulse)
//   - ResolveIdx → the structure event that confirmed it (BoS / CHoCh close)
//
// Strategies must gate on ResolveIdx, not Index, otherwise they consume OBs
// whose existence depends on candles still in the future relative to the
// current trade decision.
type OrderBlock struct {
	Index      int
	ResolveIdx int
	Top        float64
	Bottom     float64
	Bullish    bool
}

// Analysis bundles every per-candle structural observation in one place so
// strategies receive an already-computed snapshot instead of recomputing the
// same primitives independently.
type Analysis struct {
	Swings      []Swing
	FVGs        []FVG
	Structure   []StructureEvent
	OrderBlocks []OrderBlock
}
