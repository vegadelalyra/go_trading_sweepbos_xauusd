package meta

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// COMEXPinConfig parametrises the COMEX gold options expiry-pinning
// session adjustment (Phase 3.2).
//
// Microstructural rationale: COMEX GC (gold) futures and options expire
// on the last business day of every month. In the 48h leading up to
// expiry, market-makers who sold puts/calls hedge dynamically to stay
// delta-neutral, creating GRAVITATIONAL PINNING toward strikes with the
// highest open interest (typically round numbers: $2000, $2350, $2400…).
// On 5m charts during pinning regimes momentum breakouts get sucked back
// toward the pin level — directional moves are systematically suppressed
// while structural fade setups feed on the rebound.
//
// The per-strategy multipliers exploit that asymmetry:
//   - Momentum/breakout strategies (DailyBreakout, AsianRangeBO,
//     BreakerBlock — see strategy.IsLondonFixMomentum) are penalised
//     because dealer-hedging destroys the directional follow-through
//     they bet on.
//   - Mean-reversion strategies (OrderBlockMit, OTEFibonacci — see
//     strategy.IsMeanReversion) are boosted because pinning IS a fade
//     mechanism in disguise; their thesis aligns with the regime.
//
// Default zero-value (Enabled=false) is a strict no-op so threading the
// config through every ComposeSignal variant remains non-invasive — every
// legacy caller path is preserved bit-for-bit until Enabled flips true.
type COMEXPinConfig struct {
	Enabled bool
	// ExpiryDates is the set of monthly expiry timestamps to anchor the
	// pre-window. Each timestamp is interpreted as 00:00 UTC of the
	// expiry day; the pre-window starts PreWindowHours earlier and ends
	// at the expiry timestamp (half-open).
	ExpiryDates []time.Time
	// PreWindowHours is the lookback applied to each expiry date. 48 by
	// default — wide enough to capture the bulk of the pinning regime
	// without lingering into the post-expiry roll noise.
	PreWindowHours int
	// MomentumMult is the per-vote multiplier applied to momentum
	// strategies inside the pre-expiry window. 0.0 = silence them
	// entirely (the canonical setting given how aggressively pinning
	// destroys breakouts).
	MomentumMult float64
	// FadeMult is the multiplier applied to mean-reversion strategies
	// inside the pre-expiry window. 1.3 = +30% boost. 1.0 = neutral.
	FadeMult float64
}

// DefaultCOMEXPinConfig returns the canonical (48h, 0.0, 1.3) with
// Enabled=false. Callers opt-in by flipping Enabled to true and seeding
// ExpiryDates (via LoadExpiryDates) before constructing the composer.
func DefaultCOMEXPinConfig() COMEXPinConfig {
	return COMEXPinConfig{
		Enabled:        false,
		PreWindowHours: 48,
		MomentumMult:   0.0,
		FadeMult:       1.3,
	}
}

// inWindow reports whether t falls inside the half-open pre-expiry window
// [expiry - PreWindowHours, expiry) for ANY of the configured expiry
// dates. Half-open by design: an entry exactly at the expiry instant is
// post-pin (cash settlement / roll) and should not benefit from the
// fade boost.
func (c COMEXPinConfig) inWindow(t time.Time) bool {
	if !c.Enabled || len(c.ExpiryDates) == 0 || c.PreWindowHours <= 0 {
		return false
	}
	pre := time.Duration(c.PreWindowHours) * time.Hour
	tt := t.UTC()
	for _, exp := range c.ExpiryDates {
		expUTC := exp.UTC()
		start := expUTC.Add(-pre)
		// Half-open [start, exp).
		if !tt.Before(start) && tt.Before(expUTC) {
			return true
		}
	}
	return false
}

// adjust returns the weight `w` after applying the COMEX pinning
// multiplier for `stratName` when `t` falls in any pre-expiry window.
// Outside the window or when disabled, returns `w` unchanged.
func (c COMEXPinConfig) adjust(w float64, stratName string, t time.Time) float64 {
	if !c.inWindow(t) {
		return w
	}
	switch {
	case strategy.IsLondonFixMomentum(stratName):
		return w * c.MomentumMult
	case strategy.IsMeanReversion(stratName):
		return w * c.FadeMult
	}
	return w
}

// LoadExpiryDates reads the seeded comex_expiry table and returns the
// expiry timestamps as 00:00 UTC of each expiry day. The caller wires
// them into a COMEXPinConfig before constructing the composer.
//
// The table is populated by the schema/005_comex_expiry.sql migration
// (monthly last-Friday rows for the 2024-04 → 2026-05 WFA window).
// Returning a typed []time.Time keeps the package free of pgx leaking
// into COMEXPinConfig itself, mirroring how meta.LoadWeights returns
// in-memory structs rather than rows.
func LoadExpiryDates(ctx context.Context, pool *pgxpool.Pool) ([]time.Time, error) {
	rows, err := pool.Query(ctx, `
		SELECT expiry_date
		FROM comex_expiry
		ORDER BY expiry_date ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []time.Time
	for rows.Next() {
		var d time.Time
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		// Normalise to 00:00 UTC of that calendar day. pgx returns DATE
		// as a time.Time already at midnight, but the location may be
		// the session TZ; force UTC for consistency with the inWindow
		// comparator.
		out = append(out, time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC))
	}
	return out, rows.Err()
}

// LastBusinessDayOfMonth returns the last Mon-Fri date of (year, month)
// at 00:00 UTC. Useful for generating fallback expiry dates when the
// seeded table is empty or for future-proofing past 2026-05. Saturday
// (weekday 6) rolls back to Friday; Sunday (weekday 0) rolls back to
// Friday as well.
func LastBusinessDayOfMonth(year int, month time.Month) time.Time {
	// First of next month, then back up one day → last day of `month`.
	next := time.Date(year, month+1, 1, 0, 0, 0, 0, time.UTC)
	d := next.AddDate(0, 0, -1)
	for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}
