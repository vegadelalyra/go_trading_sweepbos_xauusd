package meta

import (
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// LondonFixConfig parametrises the London Gold Fix session adjustment.
//
// Microstructural rationale: XAUUSD has two daily LBMA bullion price-
// fixings (10:30 and 15:00 GMT). Around the 15:00 fix, bullion banks
// execute massive inventory-balancing orders and, to absorb that flow
// without slippage, aggressively hunt liquidity by spiking past recent
// extremes ~14:40-15:15 GMT before snapping back. On 5m candles the
// signature is a Judas swing into a swept high/low followed by violent
// mean reversion.
//
// The per-strategy multipliers exploit that asymmetry: momentum/breakout
// strategies (DailyBreakout, AsianRangeBO, BreakerBlock — see
// strategy.IsLondonFixMomentum) are penalised because breakouts inside
// the window are statistically likelier to be manipulation than
// continuation; liquidity-fade strategies (JudasSwing, TurtleSoup,
// LiquiditySweepBoS, SilverBullet — see strategy.IsLiquidityFade) are
// boosted because they're designed for exactly this regime.
//
// Default zero-value (Enabled=false) is a no-op so threading the config
// through ComposeSignal variants is a non-invasive refactor.
type LondonFixConfig struct {
	Enabled bool
	// Start / End in UTC hh:mm. Default 14:40 / 15:20.
	StartHour, StartMin int
	EndHour, EndMin     int
	// MomentumMult is the per-vote multiplier applied to momentum
	// strategies inside the window. 0.3 = 70% penalty; 0 silences them.
	MomentumMult float64
	// FadeMult is the multiplier applied to liquidity-fade strategies
	// inside the window. 1.5 = +50% boost. 1.0 = neutral.
	FadeMult float64
}

// DefaultLondonFixConfig returns the canonical (14:40, 15:20, 0.3, 1.5)
// with Enabled=false. Callers opt-in by flipping Enabled to true.
func DefaultLondonFixConfig() LondonFixConfig {
	return LondonFixConfig{
		Enabled:      false,
		StartHour:    14,
		StartMin:     40,
		EndHour:      15,
		EndMin:       20,
		MomentumMult: 0.3,
		FadeMult:     1.5,
	}
}

// inWindow reports whether t (compared in UTC) falls inside the half-open
// London Fix window [Start, End). The check is purely on hour/minute —
// the day is ignored because the fix repeats daily.
func (c LondonFixConfig) inWindow(t time.Time) bool {
	if !c.Enabled {
		return false
	}
	utc := t.UTC()
	startMins := c.StartHour*60 + c.StartMin
	endMins := c.EndHour*60 + c.EndMin
	mins := utc.Hour()*60 + utc.Minute()
	if startMins <= endMins {
		return mins >= startMins && mins < endMins
	}
	// Wrap-around window (e.g. 23:00-01:00). Not the typical case but
	// supporting it keeps the helper general.
	return mins >= startMins || mins < endMins
}

// adjust returns the weight `w` after applying the London Fix multiplier
// for `stratName` when `t` falls in the window. Outside the window or
// when disabled, returns `w` unchanged.
func (c LondonFixConfig) adjust(w float64, stratName string, t time.Time) float64 {
	if !c.inWindow(t) {
		return w
	}
	switch {
	case strategy.IsLondonFixMomentum(stratName):
		return w * c.MomentumMult
	case strategy.IsLiquidityFade(stratName):
		return w * c.FadeMult
	}
	return w
}
