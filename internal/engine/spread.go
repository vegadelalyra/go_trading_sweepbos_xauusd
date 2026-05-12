package engine

import (
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
)

// SpreadCurveConfig parametrises the synthetic intraday spread curve used
// when the engine's dynamic-spread mode is enabled. Defaults are tuned for
// Admirals XAU/USD Zero MT5 typical conditions: 0.32 base spread, 3× during
// the 17:00-18:00 EST daily rollover (NY hour 17), 2× during US news
// windows (NY 08, 09, 10, 14 — the same NY hours engine.isHighVolatilityWindow
// flags for SL slippage).
//
// Zero-value (Enabled=false) collapses every spread query to the fallback
// constant, so existing callers stay reproducible.
//
// Why a synthetic curve rather than recording real broker telemetry: until
// the live-trading bridge to Admirals ships (Phase 4.2 + FIX API in 4.3),
// we have no streaming bid/ask. The curve is a calibrated approximation
// that lets backtest and paper-replay see SPREAD VARIATION across the
// trading day instead of a flat-line $0.50 that under-prices the
// 17:00-18:00 EST rollover gap and the news-window blow-outs that destroy
// poorly-timed entries.
type SpreadCurveConfig struct {
	Enabled bool
	Base    float64 // 0.32 USD default — typical Admirals XAU/USD spread in liquid hours

	// RolloverMult multiplies Base during the 17:00-18:00 NY rollover.
	// Default 3.0 → ~ 1.0 USD across the daily futures roll.
	RolloverMult float64
	// NewsMult multiplies Base during NY 08/09/10/14 (macro release / cash
	// open / FOMC slots). Default 2.0.
	NewsMult float64

	// FilterEnabled toggles the spread-spike filter: when the spread at
	// signal time exceeds FilterMult × the trailing FilterSMABars SMA of
	// recent spreads, the signal is rejected pre-entry. Defaults are
	// 1.5× over the trailing 6 bars (30 min on 5m).
	//
	// On a synthetic curve the spike pattern is deterministic by hour;
	// the filter still validates correctly. The real value materialises
	// once live broker spread telemetry replaces the synthetic curve.
	FilterEnabled bool
	FilterMult    float64
	FilterSMABars int
}

// DefaultSpreadCurveConfig returns the canonical Admirals-tuned defaults
// with Enabled=false. Callers that want the dynamic curve must flip the
// flag explicitly.
func DefaultSpreadCurveConfig() SpreadCurveConfig {
	return SpreadCurveConfig{
		Enabled:       false,
		Base:          0.32,
		RolloverMult:  3.0,
		NewsMult:      2.0,
		FilterEnabled: false,
		FilterMult:    1.5,
		FilterSMABars: 6,
	}
}

// spreadAt returns the modelled spread for candle time t without consulting
// fallback. Exported only via EffectiveSpread; callers should not invoke
// directly because the disabled path is then ambiguous.
func (sc SpreadCurveConfig) spreadAt(t time.Time) float64 {
	hour := core.ToNY(t).Hour()
	mult := 1.0
	switch hour {
	case 17:
		// Daily futures rollover — liquidity providers withdraw, spread
		// blows out to ~1 USD on retail accounts.
		if sc.RolloverMult > 0 {
			mult = sc.RolloverMult
		}
	case 8, 9, 10, 14:
		// US macro / cash open / FOMC slot — engine.isHighVolatilityWindow
		// flags the same hours for SL slippage. Spread doubles or worse
		// just before and after the print.
		if sc.NewsMult > 0 {
			mult = sc.NewsMult
		}
	}
	return sc.Base * mult
}

// EffectiveSpread returns the dollar spread modelled for candle time t.
// When the dynamic curve is disabled, returns fallback unchanged — this
// is the legacy code path and preserves every existing tuning.
func (sc SpreadCurveConfig) EffectiveSpread(t time.Time, fallback float64) float64 {
	if !sc.Enabled {
		return fallback
	}
	return sc.spreadAt(t)
}

// SpreadSpike reports whether the modelled spread at t is more than
// FilterMult times the trailing SMA over FilterSMABars bars (one bar =
// intervalDur each). Returns false when the filter is disabled, when the
// curve itself is disabled (no per-bar spreads to average), or when
// FilterMult / FilterSMABars are degenerate.
//
// The function walks backward from t in intervalDur steps and averages the
// per-bar spreadAt values; with the deterministic synthetic curve this is
// cheap and the result is reproducible. When real broker telemetry
// replaces the synthetic curve in a future commit, the SMA inputs will
// come from a rolling buffer of observed bid/ask spreads instead.
func (sc SpreadCurveConfig) SpreadSpike(t time.Time, intervalDur time.Duration) bool {
	if !sc.Enabled || !sc.FilterEnabled {
		return false
	}
	if sc.FilterMult <= 0 || sc.FilterSMABars <= 0 || intervalDur <= 0 {
		return false
	}
	var sum float64
	for i := 1; i <= sc.FilterSMABars; i++ {
		sum += sc.spreadAt(t.Add(-time.Duration(i) * intervalDur))
	}
	sma := sum / float64(sc.FilterSMABars)
	if sma <= 0 {
		return false
	}
	return sc.spreadAt(t) > sc.FilterMult*sma
}
