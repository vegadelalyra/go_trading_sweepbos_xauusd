// Package capital simulates a fixed-risk + martingale money-management
// scheme over a pre-recorded trade stream. The strategy's edges (WR,
// R-multiples) are taken as historical fact; this package only answers
// "given THIS sizing rule, would I have survived and made money".
//
// Why separate from engine: engine sizes on RiskPct as a CONSTANT fraction
// of account, never changes per-day, never reloads. Real retail traders
// often do something different — fixed dollar amounts, doubling-down on
// losses, account reloads. Those rules don't belong in the backtest
// simulator (which is about strategy realism); they belong here.
package capital

import (
	"sort"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
)

// Tier defines a capital band with its own risk parameters. The simulator
// picks the first tier whose Threshold ≤ current capital at the start of
// each day, falling back to the Config's base FirstRiskPct/MartingaleRiskPct
// if no tier matches. Tiers MUST be sorted by Threshold ASC; the caller
// owns ordering.
//
// Why tiers exist: a trader who accepts full-Kelly risk at $280 of seed
// capital does NOT want full-Kelly at $100k — at high bankroll the goal
// shifts from max-growth to capital preservation. Stair-stepping the risk
// down at capital milestones is the canonical fractional-Kelly approach.
type Tier struct {
	Threshold         float64 // current capital ≥ this triggers this tier
	FirstRiskPct      float64
	MartingaleRiskPct float64 // 0 disables martingale at this tier
	MaxTradesPerDay   int     // 0 inherits config's base
}

// Config defines the money-management rule being simulated.
type Config struct {
	// StartCapital — initial bankroll (USD).
	StartCapital float64
	// FirstRiskPct — fraction of capital risked on the first trade of
	// the day, computed against the capital at day-start. 0.333 means
	// "risk 33.3% of what I started the day with".
	FirstRiskPct float64
	// MartingaleRiskPct — fraction of DAY-START capital risked on the
	// second trade if the first lost. This is the CANONICAL martingale
	// (double the original bet): if first risk was 33.3% of day-start,
	// the martingale is 2× that = 66.6% of day-start. Two SL hits in a
	// row burn 99.9% of the day's capital — by design, not by accident.
	//
	// Computing against day-start (not current capital) is what every
	// martingale gambling model assumes. Computing against post-loss
	// capital would softly cushion every bust, which is exactly the
	// false-comfort bug the earlier version had.
	MartingaleRiskPct float64
	// MaxTradesPerDay — hard cap on attempts per day. 2 = one martingale.
	MaxTradesPerDay int
	// ReloadThreshold — when capital falls below this, the operator
	// "reloads" the account (adds money). Setting this slightly above
	// zero handles the realistic case where you can't trade with
	// near-zero capital (positions would be sub-dust).
	ReloadThreshold float64
	// ReloadAmount — capital set TO (not added to) on reload. The
	// trader treats the reload as a fresh injection.
	ReloadAmount float64
	// Tiers — optional capital-band overrides for the risk parameters.
	// When non-empty, the simulator picks the highest-threshold tier
	// whose Threshold ≤ current capital at day-start, and uses that
	// tier's FirstRiskPct / MartingaleRiskPct in place of the base.
	// MaxTradesPerDay=0 in a tier inherits the base setting.
	Tiers []Tier
}

// pickTier returns the active tier for `capital` (highest Threshold ≤
// capital), or zero-value if none match. Tiers must be Threshold-ASC.
func (c Config) pickTier(capital float64) (Tier, bool) {
	var picked Tier
	found := false
	for _, t := range c.Tiers {
		if capital >= t.Threshold {
			picked = t
			found = true
		} else {
			break
		}
	}
	return picked, found
}

// DefaultConfig is the user's stated scheme: $280 start, 33.3% first,
// 66.6% martingale, 1 reload tier of $200 when busted.
func DefaultConfig() Config {
	return Config{
		StartCapital:      280,
		FirstRiskPct:      0.333,
		MartingaleRiskPct: 0.666,
		MaxTradesPerDay:   2,
		ReloadThreshold:   10,
		ReloadAmount:      200,
	}
}

// Result holds the post-simulation summary for one strategy (or one
// composite stream).
type Result struct {
	Strategy           string
	StartCapital       float64
	FinalCapital       float64
	Reloads            int
	TotalContribution  float64 // start + reloads*reloadAmount
	NetProfit          float64 // FinalCapital - TotalContribution
	ROIPct             float64 // NetProfit / TotalContribution × 100
	DaysWithTrades     int     // calendar days where at least one trade fired
	DaysWithMartingale int     // days where the second (martingale) trade was used
	DaysBusted         int     // days that triggered a reload
	WinDays            int     // days that ended green
	LossDays           int     // days that ended red
	MaxDrawdownPct     float64 // peak-to-trough % drop on the equity curve
	Peak               float64
}

// Simulate runs the capital scheme over `trades` and returns the
// post-simulation summary. Two realism constraints model how a manual
// retail trader actually operates:
//
//  1. Mutex — only one position can be open at a time. If a previous
//     trade's exit_time spans into the next day, that next day is
//     blocked for new entries until the position closes. This matters
//     because the backtest engine permits trades up to MaxHoldCandles
//     long (6h on 15m) which routinely cross midnight UTC.
//
//  2. Daily cap — the trader picks at most one fresh entry per day
//     (cfg.MaxTradesPerDay==1) or one fresh entry plus one martingale
//     if the first attempt lost (cfg.MaxTradesPerDay==2). No multiple
//     independent trades on the same day even if the strategy fires
//     a dozen setups: a human can only watch one position at a time.
//
// The mutex AND the daily cap are evaluated against trades in their
// recorded entry_time order — we walk the stream once and keep two
// pieces of state (freeAt = when the next position can open;
// dayUsed = attempts spent today).
func Simulate(strategyName string, trades []core.Trade, cfg Config) Result {
	res := Result{
		Strategy:          strategyName,
		StartCapital:      cfg.StartCapital,
		TotalContribution: cfg.StartCapital,
	}

	if len(trades) == 0 {
		res.FinalCapital = cfg.StartCapital
		return res
	}

	sorted := append([]core.Trade(nil), trades...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].EntryTime.Before(sorted[j].EntryTime) })

	dayOf := func(t time.Time) time.Time {
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	}

	capital := cfg.StartCapital
	res.Peak = capital

	var (
		currentDay   time.Time
		dayUsed      int  // 0=untouched, 1=first taken, 2=martingale taken
		martingaleOK bool // first trade today lost → eligible for martingale
		freeAt       time.Time
		prevDayStart float64 // capital at the start of the current day
	)

	// tallyDay closes out the books for currentDay: counts win/loss day
	// and triggers a reload if the day-end capital fell below threshold.
	// Called both at every day rollover and once after the final trade.
	tallyDay := func() {
		if currentDay.IsZero() {
			return
		}
		if capital > prevDayStart {
			res.WinDays++
		} else if capital < prevDayStart {
			res.LossDays++
		}
		if capital < cfg.ReloadThreshold {
			capital = cfg.ReloadAmount
			res.Reloads++
			res.DaysBusted++
			res.TotalContribution += cfg.ReloadAmount
		}
	}

	for _, t := range sorted {
		// Mutex: skip trades whose entry overlaps the previous trade's
		// open window. Backtest data may include strategy-overlap
		// candidates that the live trader can't take simultaneously.
		if !freeAt.IsZero() && t.EntryTime.Before(freeAt) {
			continue
		}

		d := dayOf(t.EntryTime)
		if !currentDay.Equal(d) {
			tallyDay()
			currentDay = d
			dayUsed = 0
			martingaleOK = false
			prevDayStart = capital
		}

		// Resolve the active risk parameters for this day. Tier override
		// (if any) wins over the base config; tiers were already chosen
		// when prevDayStart was captured, but we re-pick here against
		// the captured daystart so risk is consistent within the day.
		firstRisk := cfg.FirstRiskPct
		martRisk := cfg.MartingaleRiskPct
		maxTrades := cfg.MaxTradesPerDay
		if tier, ok := cfg.pickTier(prevDayStart); ok {
			firstRisk = tier.FirstRiskPct
			martRisk = tier.MartingaleRiskPct
			if tier.MaxTradesPerDay > 0 {
				maxTrades = tier.MaxTradesPerDay
			}
		}

		switch dayUsed {
		case 0:
			// First (and possibly only) trade of the day. Risk is
			// computed against THIS day's starting capital.
			risk := firstRisk * prevDayStart
			capital += t.R * risk
			dayUsed = 1
			freeAt = t.ExitTime
			martingaleOK = !t.Win
			res.DaysWithTrades++

		case 1:
			// Martingale slot — only if the first lost AND user opted in.
			// Canonical martingale: risk is computed against DAY-START
			// capital, not against the post-loss capital. So if
			// FirstRiskPct=33.3% and MartingaleRiskPct=66.6%, two SLs in
			// a row really do consume 99.9% of the day's bankroll.
			if !martingaleOK || maxTrades < 2 || martRisk <= 0 {
				continue
			}
			risk := martRisk * prevDayStart
			capital += t.R * risk
			dayUsed = 2
			freeAt = t.ExitTime
			martingaleOK = false
			res.DaysWithMartingale++

		default:
			// Day's quota used; skip every remaining trade on this day.
			continue
		}

		if capital > res.Peak {
			res.Peak = capital
		}
		if res.Peak > 0 {
			dd := (res.Peak - capital) / res.Peak
			if dd > res.MaxDrawdownPct {
				res.MaxDrawdownPct = dd
			}
		}
	}

	// Flush the final day's win/loss/reload tally.
	tallyDay()

	res.FinalCapital = capital
	res.NetProfit = capital - res.TotalContribution
	if res.TotalContribution > 0 {
		res.ROIPct = res.NetProfit / res.TotalContribution * 100
	}
	return res
}
