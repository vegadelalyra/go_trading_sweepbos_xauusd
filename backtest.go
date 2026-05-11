package main

import (
	"fmt"
	"math"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/metrics"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

type BacktestParams struct {
	StartBalance    float64
	RiskPct         float64
	DailyDDLimit    float64
	SpreadDollar    float64
	MinIndex        int
	MaxSLPoints     float64 // reject a signal whose SL distance exceeds this (points)
	MaxHoldCandles  int     // force-close after N candles open; 0 = no time stop
	CooldownCandles int     // candles to wait after a closed trade
	// F6 extensions — all optional, zero-values disable the feature.
	H4Bias    BiasMap // if non-empty, reject signals whose Side disagrees with the H4 EMA200 bias at entry time
	ATRPeriod int     // candles in ATR window (e.g. 14)
	ATRMult   float64 // if >0, widen structural SL to max(structural, ATR × ATRMult)
	// Premium/Discount zone filter. If > 0, longs are accepted only when
	// the entry sits in the lower half of the most-recent N-bar dealing
	// range, shorts only in the upper half — the smart-money "buy
	// discount, sell premium" heuristic. 96 = 24h on 15m.
	PDLookback int
	// Stochastic SL slippage. SlippagePoints is the dollar penalty applied
	// to the exit price when an SL hits — gold often gaps a few cents past
	// the stop in fast markets. SlippageNewsMult multiplies that penalty
	// during high-vol windows (NY 08-10 and 14, the typical news prints).
	// Both default to 0 / 1 so existing tunings stay unchanged.
	SlippagePoints   float64
	SlippageNewsMult float64
}

func DefaultParams() BacktestParams {
	return BacktestParams{
		StartBalance:    100_000,
		RiskPct:         0.01,
		DailyDDLimit:    0.05,
		SpreadDollar:    0.50,
		MinIndex:        50,
		MaxSLPoints:     50,
		MaxHoldCandles:  96,
		CooldownCandles: 4,
	}
}

// ParamsFor15m / ParamsFor1h / ParamsFor4h tune the per-timeframe caps so the
// same DefaultParams template still works at every interval the engine accepts.
//
// MaxHoldCandles tightens dramatically from the original 24h / 2d / 5d:
// SMC intraday setups should mature within a single session. Trades that
// linger past 6-8 hours are almost always net-negative drift, and the time
// stop pays a small mark-to-close loss instead of letting the position
// inflate the time-in-market without a reason.
func ParamsFor(interval string) BacktestParams {
	p := DefaultParams()
	switch interval {
	case "15m":
		p.MaxSLPoints = 50
		p.MaxHoldCandles = 24 // 6h
	case "1h":
		p.MaxSLPoints = 100
		p.MaxHoldCandles = 12 // 12h
	case "4h":
		p.MaxSLPoints = 200
		p.MaxHoldCandles = 6 // 24h
	}
	return p
}

type BacktestStats struct {
	Total        int
	Wins         int
	Losses       int
	WinRate      float64
	NetR         float64
	AvgR         float64
	ProfitFactor float64
	StartBalance float64
	EndBalance   float64
	MaxDDPct     float64
	PerStrategy  map[string]StrategyStats
	BySession    map[string]map[string]StrategyStats
	ByMonth      map[string]map[string]StrategyStats
	ByQuarter    map[string]map[string]StrategyStats
	Duration     map[string][]int
	RDist        map[string][]float64
	// F6 metrics.
	Sharpe            float64
	RecoveryFactor    float64
	Calmar            float64
	AnnualReturnPct   float64
	MaxLossStreak     int
	MaxWinStreak      int
	DaysCovered       int
}

type StrategyStats struct {
	Total   int
	Wins    int
	Losses  int
	NetR    float64
	WinRate float64
}

type BacktestResult struct {
	Trades []Trade
	Stats  BacktestStats
	Params BacktestParams
}

// RunBacktest evaluates `strategies` against `candles` using the supplied
// BacktestParams. Pass strategy.All() to use every registered strategy
// (production case); pass a curated subset to test a single strategy in
// isolation. Order matters — the engine fires the first match per candle.
func RunBacktest(candles []Candle, strategies []strategy.Strategy, p BacktestParams) BacktestResult {
	a := Analyze(candles)

	balance := p.StartBalance
	peak := balance
	maxDDPct := 0.0

	dayStart := balance
	currentDay := -1

	var trades []Trade
	var open *Trade
	cooldown := 0

	for i := p.MinIndex; i < len(candles); i++ {
		c := candles[i]
		ny := toNY(c.Time)
		yd := ny.YearDay()
		if yd != currentDay {
			currentDay = yd
			dayStart = balance
		}

		// daily drawdown halt: skip new entries this day
		dailyHalt := dayStart > 0 && (dayStart-balance)/dayStart >= p.DailyDDLimit

		if open != nil {
			closed := false
			slip := slPenalty(p, c.Time)
			if open.Side == Long {
				if c.Low <= open.SL {
					if slip > 0 {
						// Worst-case fill: cap at c.Low so a gap past
						// SL+slip lands wherever price actually went.
						exit := open.SL - slip
						if c.Low < exit {
							exit = c.Low
						}
						open.ExitPrice = exit - p.SpreadDollar
						riskDist := open.Entry - open.SL
						actualLoss := open.Entry - exit + p.SpreadDollar
						open.R = -actualLoss / riskDist
					} else {
						// No slippage modelled — clean -1R fill at SL.
						open.ExitPrice = open.SL - p.SpreadDollar
						open.R = -1
					}
					open.Win = false
					closed = true
				} else if c.High >= open.TP {
					open.ExitPrice = open.TP - p.SpreadDollar
					open.R = (open.TP - open.Entry) / (open.Entry - open.SL)
					open.Win = true
					closed = true
				}
			} else {
				if c.High >= open.SL {
					if slip > 0 {
						exit := open.SL + slip
						if c.High > exit {
							exit = c.High
						}
						open.ExitPrice = exit + p.SpreadDollar
						riskDist := open.SL - open.Entry
						actualLoss := exit - open.Entry + p.SpreadDollar
						open.R = -actualLoss / riskDist
					} else {
						open.ExitPrice = open.SL + p.SpreadDollar
						open.R = -1
					}
					open.Win = false
					closed = true
				} else if c.Low <= open.TP {
					open.ExitPrice = open.TP + p.SpreadDollar
					open.R = (open.Entry - open.TP) / (open.SL - open.Entry)
					open.Win = true
					closed = true
				}
			}
			// Time-stop: if the trade has been open too long, exit at this candle's close.
			// The P/L is whatever fraction of risk we've banked so far.
			if !closed && p.MaxHoldCandles > 0 && i-open.EntryIdx >= p.MaxHoldCandles {
				open.ExitPrice = c.Close
				if open.Side == Long {
					riskDist := open.Entry - open.SL
					if riskDist > 0 {
						open.R = (c.Close - open.Entry) / riskDist
					}
				} else {
					riskDist := open.SL - open.Entry
					if riskDist > 0 {
						open.R = (open.Entry - c.Close) / riskDist
					}
				}
				open.Win = open.R > 0
				closed = true
			}
			if closed {
				risk := balance * p.RiskPct
				balance += risk * open.R
				open.ExitIdx = i
				open.ExitTime = c.Time
				open.BalanceAfter = balance
				open.Session = classifySession(open.EntryTime)
				open.YearMonth = yearMonthBucket(open.EntryTime)
				open.YearQuarter = yearQuarterBucket(open.EntryTime)
				open.CandlesToExit = open.ExitIdx - open.EntryIdx
				trades = append(trades, *open)
				open = nil
				cooldown = p.CooldownCandles

				if balance > peak {
					peak = balance
				}
				if peak > 0 {
					dd := (peak - balance) / peak
					if dd > maxDDPct {
						maxDDPct = dd
					}
				}
			}
			continue
		}

		if cooldown > 0 {
			cooldown--
			continue
		}
		if dailyHalt {
			continue
		}

		for _, s := range strategies {
			sig, ok := s.Detect(candles, a, i)
			if !ok {
				continue
			}
			// F6 — Multi-TF bias filter: reject signals fighting the H4 trend.
			// Lookup returns 0 when the BiasMap is empty OR when bias is neutral —
			// both cases pass through without filtering.
			if bias := p.H4Bias.Lookup(candles[i].Time); bias != 0 {
				if bias > 0 && sig.Side == Short {
					continue
				}
				if bias < 0 && sig.Side == Long {
					continue
				}
			}
			// F6 — Optional ATR-based SL widening: keep the larger of the
			// strategy's structural SL and ATR × session-adjusted-mult.
			// Asia is structurally tighter (smaller true ranges) so we
			// shrink the multiplier there; NY-PM and the dead "off" band
			// also drift quieter. London and NY-AM keep the full multiplier.
			if p.ATRMult > 0 && p.ATRPeriod > 0 {
				atr := ATRAt(candles, i, p.ATRPeriod)
				if atr > 0 {
					mult := p.ATRMult * sessionATRScale(c.Time)
					structuralD := math.Abs(sig.Entry - sig.SL)
					atrD := atr * mult
					if atrD > structuralD {
						if sig.Side == Long {
							sig.SL = sig.Entry - atrD
							sig.TP = sig.Entry + atrD*strategy.DefaultRR
						} else {
							sig.SL = sig.Entry + atrD
							sig.TP = sig.Entry - atrD*strategy.DefaultRR
						}
					}
				}
			}
			// Premium/Discount filter: only long in the lower half of the
			// dealing range, only short in the upper half. Fades the
			// retail-trap signals that would have entered on the wrong
			// side of institutional value.
			if p.PDLookback > 0 {
				dr := smc.CurrentDealingRange(candles, i, p.PDLookback)
				if sig.Side == Long && dr.InPremium(sig.Entry) {
					continue
				}
				if sig.Side == Short && dr.InDiscount(sig.Entry) {
					continue
				}
			}
			// Engine-level guard: reject signals whose SL distance is unreasonable.
			if p.MaxSLPoints > 0 {
				slDist := math.Abs(sig.Entry - sig.SL)
				if slDist > p.MaxSLPoints {
					continue
				}
			}
			if sig.Side == Long {
				sig.Entry += p.SpreadDollar
			} else {
				sig.Entry -= p.SpreadDollar
			}
			t := Trade{
				Strategy:  sig.Strategy,
				Side:      sig.Side,
				EntryIdx:  i,
				EntryTime: c.Time,
				Entry:     sig.Entry,
				SL:        sig.SL,
				TP:        sig.TP,
			}
			open = &t
			break
		}
	}

	return BacktestResult{
		Trades: trades,
		Stats:  computeStats(trades, p, maxDDPct, balance),
		Params: p,
	}
}

func computeStats(trades []Trade, p BacktestParams, maxDDPct, endBal float64) BacktestStats {
	s := BacktestStats{
		StartBalance: p.StartBalance,
		EndBalance:   endBal,
		MaxDDPct:     maxDDPct,
		PerStrategy:  map[string]StrategyStats{},
		BySession:    map[string]map[string]StrategyStats{},
		ByMonth:      map[string]map[string]StrategyStats{},
		ByQuarter:    map[string]map[string]StrategyStats{},
		Duration:     map[string][]int{},
		RDist:        map[string][]float64{},
	}
	var grossWin, grossLoss float64
	for _, t := range trades {
		s.Total++
		if t.Win {
			s.Wins++
			grossWin += t.R
		} else {
			s.Losses++
			grossLoss += -t.R
		}
		s.NetR += t.R

		bumpStats(s.PerStrategy, t.Strategy, t)
		bumpNestedStats(s.BySession, t.Strategy, t.Session, t)
		monthKey := fmt.Sprintf("%04d-%02d", t.YearMonth/100, t.YearMonth%100)
		bumpNestedStats(s.ByMonth, t.Strategy, monthKey, t)
		quarterKey := fmt.Sprintf("%04dQ%d", t.YearQuarter/10, t.YearQuarter%10)
		bumpNestedStats(s.ByQuarter, t.Strategy, quarterKey, t)

		s.Duration[t.Strategy] = append(s.Duration[t.Strategy], t.CandlesToExit)
		s.RDist[t.Strategy] = append(s.RDist[t.Strategy], t.R)
	}
	if s.Total > 0 {
		s.WinRate = float64(s.Wins) / float64(s.Total)
		s.AvgR = s.NetR / float64(s.Total)
	}
	if grossLoss > 0 {
		s.ProfitFactor = grossWin / grossLoss
	} else if grossWin > 0 {
		s.ProfitFactor = math.Inf(1)
	}

	// F6 metrics — Sharpe / Recovery / Calmar / streaks.
	if len(trades) > 0 {
		rs := make([]float64, 0, len(trades))
		for _, t := range trades {
			rs = append(rs, t.R)
		}
		days := int(trades[len(trades)-1].ExitTime.Sub(trades[0].EntryTime).Hours() / 24)
		if days < 1 {
			days = 1
		}
		s.DaysCovered = days
		tradesPerYear := float64(len(trades)) * 365.0 / float64(days)
		s.Sharpe = metrics.SharpeRatio(rs, tradesPerYear)
		s.RecoveryFactor = metrics.RecoveryFactor(s.NetR, maxDDPct)
		s.AnnualReturnPct = metrics.AnnualizedReturn(s.StartBalance, s.EndBalance, days)
		s.Calmar = metrics.CalmarRatio(s.AnnualReturnPct, maxDDPct*100)
		s.MaxLossStreak = metrics.MaxConsecutiveLosses(rs)
		s.MaxWinStreak = metrics.MaxConsecutiveWins(rs)
	}
	return s
}

func bumpStats(m map[string]StrategyStats, key string, t Trade) {
	s := m[key]
	s.Total++
	if t.Win {
		s.Wins++
	} else {
		s.Losses++
	}
	s.NetR += t.R
	if s.Total > 0 {
		s.WinRate = float64(s.Wins) / float64(s.Total)
	}
	m[key] = s
}

// slPenalty returns the dollar slippage applied to an SL-hit exit price for
// candle c. Default (SlippagePoints == 0) preserves the old behaviour of
// filling at the SL exactly. When set, the engine pretends price gapped a
// few cents past the stop — and multiplies the penalty during the news
// windows that produce most gold gap-and-go moves.
func slPenalty(p BacktestParams, t time.Time) float64 {
	if p.SlippagePoints <= 0 {
		return 0
	}
	if isHighVolatilityWindow(t) && p.SlippageNewsMult > 0 {
		return p.SlippagePoints * p.SlippageNewsMult
	}
	return p.SlippagePoints
}

// isHighVolatilityWindow flags the NY hours where US data, equity opens
// and overlapping liquidity print most of gold's intraday range-extension
// moves: 08 (early macro releases), 09 (cash open), 10 (ISM/JOLTS slot)
// and 14 (FOMC / PPI / late releases).
func isHighVolatilityWindow(t time.Time) bool {
	h := toNY(t).Hour()
	return h == 8 || h == 9 || h == 10 || h == 14
}

// sessionATRScale returns the multiplier applied to BacktestParams.ATRMult
// based on the candle's session. The numbers come from measuring 15m ATR
// medians per session over the 2y XAU dataset — Asia prints roughly 0.67×
// the London/NY range, NY-PM trails at 0.85×, the dead band at 0.80×.
// Returning 1.0 for the active sessions keeps existing tunings intact.
func sessionATRScale(t time.Time) float64 {
	switch classifySession(t) {
	case SessionAsia:
		return 0.67
	case SessionLondon, SessionNYAM:
		return 1.0
	case SessionNYPM:
		return 0.85
	default:
		return 0.80
	}
}

func bumpNestedStats(m map[string]map[string]StrategyStats, strategy, bucket string, t Trade) {
	if m[strategy] == nil {
		m[strategy] = make(map[string]StrategyStats)
	}
	bumpStats(m[strategy], bucket, t)
}

func (s BacktestStats) Summary() string {
	pf := fmt.Sprintf("%.2f", s.ProfitFactor)
	if math.IsInf(s.ProfitFactor, 1) {
		pf = "∞"
	}
	return fmt.Sprintf(
		"Trades: %d | WinRate: %.1f%% | NetR: %+.2f | AvgR: %+.2f | PF: %s | DD: %.1f%% | $%.0f→$%.0f",
		s.Total, s.WinRate*100, s.NetR, s.AvgR, pf, s.MaxDDPct*100, s.StartBalance, s.EndBalance,
	)
}
