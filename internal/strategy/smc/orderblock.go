package smc

import (
	"fmt"
	"math"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/indicator"
	prim "github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// OrderBlockMitConfig captures every tunable knob of the OrderBlock-mitigation
// entry. Tweaking a field here has no effect on any other strategy.
type OrderBlockMitConfig struct {
	MinOBSize float64 // dollars; OBs smaller than this are noise on 15m
	SLBuffer  float64 // pad beyond the OB extreme
	RR        float64 // target reward:risk
	MinAge    int     // wait this many candles before touching an OB
	MaxAge    int     // beyond this it's stale

	// Phase 1.4 validation gates — canonical SMC zone-and-displacement filters.
	// The primitive's three pillars (sweep, FVG-presence, mitigation-zero)
	// are necessary but not sufficient: in wfa#9 OBM produced 112 OOS trades
	// at 66% WR, leaving plenty of marginal setups. These gates discard the
	// weakest by quantifying both the OB candle's character and the impulse
	// that confirmed it.
	//
	// WickRatioThreshold — if the OB origin candle's rejection wick is at
	// least this fraction of its full range, the zone collapses onto the wick
	// (rejection block); otherwise the body alone is the zone. Default 0.40
	// is the canonical ICT threshold between "indecision" and "rejection".
	//
	// MinDisplacementBodyRatio — the impulsive candle confirming the OB must
	// have a body that is at least this fraction of its full range. 0.70
	// rejects half-body / wickey "impulse" bars that look directional only
	// in hindsight.
	//
	// MinDisplacementATRMult — the same impulsive candle's full range must
	// be at least this multiple of ATR(ATRPeriod) at that index. 1.5×
	// enforces that the displacement is statistically meaningful relative
	// to ambient volatility, not just a normal bar.
	//
	// ATRPeriod — Wilder ATR lookback used for the displacement check.
	WickRatioThreshold       float64
	MinDisplacementBodyRatio float64
	MinDisplacementATRMult   float64
	ATRPeriod                int
}

// DefaultOrderBlockMitConfig returns the production-tuned defaults derived
// from the run-#2 1873-trade flood post-mortem, plus the Phase 1.4 stricter
// zone / displacement gates (wick 0.40, body 0.70, displacement ≥1.5×ATR14).
// The gates are ON by default — Phase 1.4's whole purpose is to validate
// whether they lift OBM's OOS WR toward 70-72% without bleeding NetR.
func DefaultOrderBlockMitConfig() OrderBlockMitConfig {
	return OrderBlockMitConfig{
		MinOBSize:                3.0,
		SLBuffer:                 strategy.DefaultSLBuffer,
		RR:                       strategy.DefaultRR,
		MinAge:                   5,
		MaxAge:                   50,
		WickRatioThreshold:       0.40,
		MinDisplacementBodyRatio: 0.70,
		MinDisplacementATRMult:   1.5,
		ATRPeriod:                14,
	}
}

// OrderBlockMit detects price returning to an unmitigated Order Block produced
// by an earlier BoS. Enter at the OB midpoint, SL just beyond the far edge.
// Requires an inducement (sub-swing sweep between the OB and current price)
// to confirm institutional fuel — without it the touch is more likely a
// continuation than a true mitigation.
type OrderBlockMit struct {
	Cfg OrderBlockMitConfig
}

func (s *OrderBlockMit) Meta() strategy.Meta {
	return strategy.Meta{
		Name:        "OrderBlockMit",
		Family:      strategy.FamilySMC,
		Description: "Mitigation entry at unfilled OB midpoint with inducement gate",
	}
}

func (s *OrderBlockMit) Detect(candles []core.Candle, a prim.Analysis, i int) (core.Signal, bool) {
	if i < 30 {
		return core.Signal{}, false
	}
	cur := candles[i]

	for k := len(a.OrderBlocks) - 1; k >= 0; k-- {
		ob := a.OrderBlocks[k]
		if ob.Index >= i {
			continue
		}
		// OB only "exists" once the confirming structure event has closed.
		if ob.ResolveIdx >= i {
			continue
		}
		age := i - ob.Index
		if age > s.Cfg.MaxAge {
			break
		}
		if age < s.Cfg.MinAge {
			continue
		}

		bull := ob.Bullish

		// Gate A — wick/body discrimination on the OB origin candle. Collapse
		// the raw full-candle zone (primitive returns ob.Low..ob.High) onto
		// either the rejection wick or the body, never both. Canonical SMC:
		// a wick-dominant bar IS a rejection block, a body-dominant bar's
		// imbalance lives in its body.
		zoneBottom, zoneTop, isRejection := refineOBZone(candles[ob.Index], bull, s.Cfg.WickRatioThreshold)

		size := zoneTop - zoneBottom
		if size < s.Cfg.MinOBSize {
			continue
		}

		// Gate B — displacement quantification. The impulsive candle that
		// confirmed the OB must have a dominant body (≥ MinDisplacementBodyRatio)
		// AND a full range ≥ MinDisplacementATRMult × ATR(ATRPeriod). Either
		// fails → drop the OB; we will not trade against a soft impulse.
		if !hasValidDisplacement(candles, ob.Index, bull, s.Cfg) {
			continue
		}

		// OB midpoint must not have been mitigated already.
		mid := (zoneTop + zoneBottom) / 2
		mitigated := false
		for j := ob.Index + 1; j < i; j++ {
			if candles[j].Low <= mid && candles[j].High >= mid {
				mitigated = true
				break
			}
		}
		if mitigated {
			continue
		}

		if cur.Low > zoneTop || cur.High < zoneBottom {
			continue
		}

		// Inducement gate.
		if !hasInducement(candles, ob.Index, i, bull) {
			continue
		}

		entry := mid
		// SL anchors at the OB candle's far structural extreme (the full wick),
		// not the refined zone edge — the wick is what price actually
		// disrespected during the original sweep and remains the safe pad.
		var sl float64
		if bull {
			sl = ob.Bottom - s.Cfg.SLBuffer
		} else {
			sl = ob.Top + s.Cfg.SLBuffer
		}
		tp := strategy.TPFromRR(entry, sl, s.Cfg.RR, bull)
		kind := "body"
		if isRejection {
			kind = "wick"
		}
		return strategy.BuildSignal(
			"OrderBlockMit", bull, entry, sl, tp, i, cur.Time,
			fmt.Sprintf("OB@%d %s-zone=%.2f-%.2f size=%.1f", ob.Index, kind, zoneBottom, zoneTop, size),
		)
	}
	return core.Signal{}, false
}

// refineOBZone applies Phase-1.4 Gate A: for a wick-dominant OB candle
// (rejection wick ≥ wickThreshold of the candle's full range) the zone IS
// the rejection wick; otherwise the zone is the body. Returns
// (bottom, top, isRejection). bull=true → bullish OB (down-candle with lower
// rejection wick); bull=false → bearish OB (up-candle with upper rejection
// wick).
func refineOBZone(c core.Candle, bull bool, wickThreshold float64) (float64, float64, bool) {
	full := c.High - c.Low
	bodyTop := math.Max(c.Open, c.Close)
	bodyBot := math.Min(c.Open, c.Close)

	if full <= 0 {
		// Degenerate doji-of-zero-range; fall back to body (which is also a
		// point). The OB size gate will discard it.
		return bodyBot, bodyTop, false
	}

	var rejWick float64
	if bull {
		rejWick = bodyBot - c.Low // lower wick on a bullish OB
	} else {
		rejWick = c.High - bodyTop // upper wick on a bearish OB
	}
	wickRatio := rejWick / full

	if wickRatio > wickThreshold {
		if bull {
			return c.Low, bodyBot, true
		}
		return bodyTop, c.High, true
	}
	return bodyBot, bodyTop, false
}

// hasValidDisplacement applies Phase-1.4 Gate B: at least one candle inside
// [obIdx+1, obIdx+3] (the FVG-confirmation window already enforced by the
// primitive) must be a same-direction impulse with body ratio ≥
// MinDisplacementBodyRatio AND full range ≥ MinDisplacementATRMult × ATR at
// that index. If no candle in the window qualifies, the OB is rejected
// outright — the primitive's "an FVG exists nearby" check is too lenient.
func hasValidDisplacement(candles []core.Candle, obIdx int, bull bool, cfg OrderBlockMitConfig) bool {
	for j := obIdx + 1; j <= obIdx+3 && j < len(candles); j++ {
		c := candles[j]
		full := c.High - c.Low
		if full <= 0 {
			continue
		}
		// Must travel in the direction the OB predicts.
		if bull && c.Close <= c.Open {
			continue
		}
		if !bull && c.Close >= c.Open {
			continue
		}
		bodyRatio := math.Abs(c.Close-c.Open) / full
		if bodyRatio < cfg.MinDisplacementBodyRatio {
			continue
		}
		atr := indicator.ATRAt(candles, j, cfg.ATRPeriod)
		if atr <= 0 {
			continue
		}
		if full < cfg.MinDisplacementATRMult*atr {
			continue
		}
		return true
	}
	return false
}

// hasInducement scans (obIdx, i) for any 3-bar local extreme that was later
// pierced within the same window. For bullish setups we want a swept LOW
// (stop run below), for bearish a swept HIGH.
func hasInducement(candles []core.Candle, obIdx, i int, bull bool) bool {
	for m := obIdx + 1; m < i; m++ {
		if m-1 < 0 || m+1 >= len(candles) {
			continue
		}
		if bull && candles[m].Low < candles[m-1].Low && candles[m].Low < candles[m+1].Low {
			for n := m + 1; n < i; n++ {
				if candles[n].Low < candles[m].Low {
					return true
				}
			}
		}
		if !bull && candles[m].High > candles[m-1].High && candles[m].High > candles[m+1].High {
			for n := m + 1; n < i; n++ {
				if candles[n].High > candles[m].High {
					return true
				}
			}
		}
	}
	return false
}
