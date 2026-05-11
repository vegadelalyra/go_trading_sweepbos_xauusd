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

// Config defines the money-management rule being simulated.
type Config struct {
	// StartCapital — initial bankroll (USD).
	StartCapital float64
	// FirstRiskPct — fraction of current capital risked on the first trade
	// of the day. 0.333 means "risk 33.3% of whatever I have right now".
	FirstRiskPct float64
	// MartingaleRiskPct — fraction of REMAINING capital risked on the
	// second trade if the first lost. Martingale-style doubling-down.
	// 0.666 means "risk 66.6% of what's left after the first loss".
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

// Simulate runs the capital scheme over `trades` (expected pre-sorted by
// entry_time, but we sort defensively) and returns the post-simulation
// summary. Trades are bucketed by calendar day in their entry-time's
// local zone; the user picks at most MaxTradesPerDay per day, taking
// trades in time-order.
func Simulate(strategy string, trades []core.Trade, cfg Config) Result {
	res := Result{
		Strategy:          strategy,
		StartCapital:      cfg.StartCapital,
		TotalContribution: cfg.StartCapital,
	}

	if len(trades) == 0 {
		res.FinalCapital = cfg.StartCapital
		return res
	}

	sorted := append([]core.Trade(nil), trades...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].EntryTime.Before(sorted[j].EntryTime) })

	// Group by UTC calendar day. We use UTC because backtest_trades
	// stores entry_time in UTC; the trader's local day boundary is a
	// rough approximation but stable across the dataset.
	byDay := make(map[time.Time][]core.Trade)
	for _, t := range sorted {
		d := time.Date(t.EntryTime.Year(), t.EntryTime.Month(), t.EntryTime.Day(), 0, 0, 0, 0, time.UTC)
		byDay[d] = append(byDay[d], t)
	}
	days := make([]time.Time, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })

	capital := cfg.StartCapital
	res.Peak = capital
	for _, d := range days {
		dayTrades := byDay[d]
		dayStart := capital
		tookSecond := false

		// First attempt of the day.
		if len(dayTrades) >= 1 && capital > 0 {
			risk := cfg.FirstRiskPct * capital
			capital += dayTrades[0].R * risk
			// Martingale only fires when the first attempt lost AND
			// we still have inventory of trades for the day AND we
			// still have positive capital to risk.
			if !dayTrades[0].Win && cfg.MaxTradesPerDay >= 2 && len(dayTrades) >= 2 && capital > 0 {
				risk2 := cfg.MartingaleRiskPct * capital
				capital += dayTrades[1].R * risk2
				tookSecond = true
			}
			res.DaysWithTrades++
			if tookSecond {
				res.DaysWithMartingale++
			}
		}

		// Reload after the day's P&L crystallises. Threshold check
		// avoids reloading when capital is just dented (the trader
		// only refills on a real bust).
		if capital < cfg.ReloadThreshold {
			capital = cfg.ReloadAmount
			res.Reloads++
			res.DaysBusted++
			res.TotalContribution += cfg.ReloadAmount
		}

		if capital > dayStart {
			res.WinDays++
		} else if capital < dayStart {
			res.LossDays++
		}

		// Drawdown tracked on the equity curve (capital + cumulative
		// reload contribution — measures the trader's true exposure
		// across the run).
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

	res.FinalCapital = capital
	res.NetProfit = capital - res.TotalContribution
	if res.TotalContribution > 0 {
		res.ROIPct = res.NetProfit / res.TotalContribution * 100
	}
	return res
}
