package main

import (
	"fmt"
	"math"
	"time"
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
func ParamsFor(interval string) BacktestParams {
	p := DefaultParams()
	switch interval {
	case "15m":
		p.MaxSLPoints = 50
		p.MaxHoldCandles = 96 // 24h
	case "1h":
		p.MaxSLPoints = 100
		p.MaxHoldCandles = 48 // 2 days
	case "4h":
		p.MaxSLPoints = 200
		p.MaxHoldCandles = 30 // 5 days
	}
	return p
}

type Trade struct {
	Strategy      string
	Side          Side
	EntryIdx      int
	ExitIdx       int
	EntryTime     time.Time
	ExitTime      time.Time
	Entry         float64
	SL            float64
	TP            float64
	ExitPrice     float64
	R             float64
	Win           bool
	BalanceAfter  float64
	Session       string
	YearMonth     int
	YearQuarter   int
	CandlesToExit int
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

func RunBacktest(candles []Candle, strategies []StrategyFn, p BacktestParams) BacktestResult {
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
			if open.Side == Long {
				if c.Low <= open.SL {
					open.ExitPrice = open.SL - p.SpreadDollar
					open.R = -1
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
					open.ExitPrice = open.SL + p.SpreadDollar
					open.R = -1
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

		for _, strat := range strategies {
			sig, ok := strat(candles, a, i)
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
			// strategy's structural SL and ATR×k. Helps in choppy regimes.
			if p.ATRMult > 0 && p.ATRPeriod > 0 {
				atr := ATRAt(candles, i, p.ATRPeriod)
				if atr > 0 {
					structuralD := math.Abs(sig.Entry - sig.SL)
					atrD := atr * p.ATRMult
					if atrD > structuralD {
						if sig.Side == Long {
							sig.SL = sig.Entry - atrD
						} else {
							sig.SL = sig.Entry + atrD
						}
						// Recompute TP to keep fixed RR.
						if sig.Side == Long {
							sig.TP = sig.Entry + atrD*fixedRR
						} else {
							sig.TP = sig.Entry - atrD*fixedRR
						}
					}
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
		s.Sharpe = SharpeRatio(rs, tradesPerYear)
		s.RecoveryFactor = RecoveryFactor(s.NetR, maxDDPct)
		s.AnnualReturnPct = AnnualizedReturn(s.StartBalance, s.EndBalance, days)
		s.Calmar = CalmarRatio(s.AnnualReturnPct, maxDDPct*100)
		s.MaxLossStreak = MaxConsecutiveLosses(rs)
		s.MaxWinStreak = MaxConsecutiveWins(rs)
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
