package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/smc"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

// WFAConfig parametrises a single walk-forward analysis run.
//
// Time windows roll forward in StepMonths increments. Inside each window
// we (a) backtest the full strategy set on the IS slice and derive per-strategy
// weights from those results, then (b) replay the OOS slice using ComposeSignal
// gated by those weights — the model never sees its own evaluation data. The
// OOS trades from every window are concatenated to form the "honest curve".
type WFAConfig struct {
	Symbol          string
	Interval        string
	ISWindowMonths  int       // in-sample window (weights derive from this)
	OOSWindowMonths int       // out-of-sample window (evaluation only)
	StepMonths      int       // gap between successive IS starts
	StartDate       time.Time // zero = beginning of available data
	EndDate         time.Time // zero = up to most recent candle
	Threshold       float64   // |NetWeight| required for an OOS entry
	UseBias         bool
	ATRMult         float64
	H4Period        int
	PDLookback      int // Premium/Discount filter lookback in candles (0 disables)
	Slippage        float64
	SlippageNewsMult float64
}

// WFAWindow holds one IS→OOS slice plus the OOS-side performance.
type WFAWindow struct {
	ISStart, ISEnd   time.Time
	OOSStart, OOSEnd time.Time
	ISWeights        map[string]float64
	OOSTrades        []Trade
	OOSNetR          float64
	OOSWinRate       float64
	OOSSharpe        float64
}

// WFAResult is the aggregate of every window run plus the concatenated OOS curve.
type WFAResult struct {
	Windows        []WFAWindow
	CombinedTrades []Trade
	CombinedStats  BacktestStats
	WindowsCount   int
}

// RunWFA orchestrates the walk-forward analysis end-to-end.
func RunWFA(ctx context.Context, pool *pgxpool.Pool, cfg WFAConfig) (WFAResult, error) {
	allCandles, err := loadCandles(ctx, pool, cfg.Symbol, cfg.Interval, cfg.StartDate, cfg.EndDate)
	if err != nil {
		return WFAResult{}, fmt.Errorf("load candles: %w", err)
	}
	if len(allCandles) < 5000 {
		return WFAResult{}, fmt.Errorf("need at least 5000 candles for WFA, got %d", len(allCandles))
	}

	// Pre-load H4 candles once. The per-window slice is taken later so each
	// window's BiasMap only sees bars up to its own oosEnd.
	var h4Candles []Candle
	if cfg.UseBias {
		h4Candles, err = loadCandles(ctx, pool, cfg.Symbol, "4h", cfg.StartDate, cfg.EndDate)
		if err != nil {
			return WFAResult{}, fmt.Errorf("load h4: %w", err)
		}
	}

	var result WFAResult
	t0 := allCandles[0].Time
	tEnd := allCandles[len(allCandles)-1].Time

	for cursor := t0; cursor.AddDate(0, cfg.ISWindowMonths+cfg.OOSWindowMonths, 0).Before(tEnd); cursor = cursor.AddDate(0, cfg.StepMonths, 0) {
		isStart := cursor
		isEnd := cursor.AddDate(0, cfg.ISWindowMonths, 0)
		oosStart := isEnd
		oosEnd := isEnd.AddDate(0, cfg.OOSWindowMonths, 0)
		if oosEnd.After(tEnd) {
			oosEnd = tEnd
		}

		isCandles := sliceByTime(allCandles, isStart, isEnd)
		if len(isCandles) < 500 {
			continue
		}
		oosCandles := sliceByTime(allCandles, oosStart, oosEnd)
		if len(oosCandles) < 100 {
			continue
		}

		params := ParamsFor(cfg.Interval)
		if cfg.UseBias && len(h4Candles) > 0 {
			h4Slice := sliceByTime(h4Candles, isStart, oosEnd)
			params.H4Bias = BuildBiasMap(h4Slice, cfg.H4Period, 4*time.Hour)
		}
		if cfg.ATRMult > 0 {
			params.ATRMult = cfg.ATRMult
			params.ATRPeriod = 14
		}
		params.PDLookback = cfg.PDLookback
		params.SlippagePoints = cfg.Slippage
		params.SlippageNewsMult = cfg.SlippageNewsMult

		isResult := RunBacktest(isCandles, strategy.All(), params)
		weightsList := computeWeights(isResult.Trades)
		weights := make(map[string]float64, len(weightsList))
		for _, w := range weightsList {
			weights[w.Strategy] = w.Weight
		}

		oosTrades := simulateOOS(oosCandles, params, weights, cfg.Threshold)

		win := WFAWindow{
			ISStart:   isStart,
			ISEnd:     isEnd,
			OOSStart:  oosStart,
			OOSEnd:    oosEnd,
			ISWeights: weights,
			OOSTrades: oosTrades,
		}
		summarizeWFAWindow(&win)
		result.Windows = append(result.Windows, win)
		result.CombinedTrades = append(result.CombinedTrades, oosTrades...)
	}

	result.WindowsCount = len(result.Windows)
	if len(result.CombinedTrades) > 0 {
		result.CombinedStats = combinedStats(result.CombinedTrades, ParamsFor(cfg.Interval))
	}
	return result, nil
}

// sliceByTime returns the half-open slice [from, to) of a chronologically-sorted
// candle slice. We binary-search both boundaries; the input must be sorted by
// Time ascending (loadCandles already does this).
func sliceByTime(candles []Candle, from, to time.Time) []Candle {
	lo := sort.Search(len(candles), func(i int) bool { return !candles[i].Time.Before(from) })
	hi := sort.Search(len(candles), func(i int) bool { return !candles[i].Time.Before(to) })
	if lo >= hi {
		return nil
	}
	return candles[lo:hi]
}

// computeWeights derives StrategyWeights from an in-memory trades slice.
// Mirrors the SQL aggregation in LoadWeights so WFA windows don't need to
// persist their IS run to Postgres. Uses the same sharpeWeight helper as
// LoadWeights — drift between the two would silently bias WFA results.
func computeWeights(trades []Trade) []StrategyWeight {
	byStrat := map[string][]Trade{}
	for _, t := range trades {
		byStrat[t.Strategy] = append(byStrat[t.Strategy], t)
	}
	out := make([]StrategyWeight, 0, len(byStrat))
	for name, ts := range byStrat {
		n := len(ts)
		exp := 0.0
		wins := 0
		netR := 0.0
		for _, t := range ts {
			exp += t.R
			netR += t.R
			if t.Win {
				wins++
			}
		}
		exp /= float64(n)

		// Population stddev — same convention as SharpeRatio in metrics.go
		// and STDDEV_POP in the SQL aggregation.
		sq := 0.0
		for _, t := range ts {
			d := t.R - exp
			sq += d * d
		}
		stddev := math.Sqrt(sq / float64(n))

		out = append(out, StrategyWeight{
			Strategy:   name,
			Trades:     n,
			Expectancy: exp,
			StdDev:     stddev,
			WinRate:    float64(wins) / float64(n),
			NetR:       netR,
			Weight:     sharpeWeight(exp, stddev, n),
		})
	}
	return out
}

// simulateOOS replays a candle slice using the composite-signal gate. Unlike
// RunBacktest (which fires the first matching strategy per candle), here every
// strategy votes and only the weighted majority — already known from the IS
// window — produces an entry. The lifecycle (SL/TP/time-stop/spread/ATR/SL guard)
// matches RunBacktest exactly so OOS realism is preserved.
func simulateOOS(candles []Candle, p BacktestParams, weights map[string]float64, threshold float64) []Trade {
	a := Analyze(candles)
	balance := p.StartBalance
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
		dailyHalt := dayStart > 0 && (dayStart-balance)/dayStart >= p.DailyDDLimit

		if open != nil {
			closed := false
			slip := slPenalty(p, c.Time)
			if open.Side == Long {
				if c.Low <= open.SL {
					if slip > 0 {
						exit := open.SL - slip
						if c.Low < exit {
							exit = c.Low
						}
						open.ExitPrice = exit - p.SpreadDollar
						riskDist := open.Entry - open.SL
						actualLoss := open.Entry - exit + p.SpreadDollar
						open.R = -actualLoss / riskDist
					} else {
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
			if !closed && p.MaxHoldCandles > 0 && i-open.EntryIdx >= p.MaxHoldCandles {
				open.ExitPrice = c.Close
				if open.Side == Long {
					rd := open.Entry - open.SL
					if rd > 0 {
						open.R = (c.Close - open.Entry) / rd
					}
				} else {
					rd := open.SL - open.Entry
					if rd > 0 {
						open.R = (open.Entry - c.Close) / rd
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

		cs := ComposeSignal(candles, a, i, weights, threshold)
		if !cs.Recommended {
			continue
		}
		// Bias filter — match RunBacktest's contract: 0 = unknown/neutral passes.
		if bias := p.H4Bias.Lookup(c.Time); bias != 0 {
			if bias > 0 && cs.Side == Short {
				continue
			}
			if bias < 0 && cs.Side == Long {
				continue
			}
		}

		// Premium/Discount filter on the composite entry.
		if p.PDLookback > 0 {
			dr := smc.CurrentDealingRange(candles, i, p.PDLookback)
			if cs.Side == Long && dr.InPremium(cs.BestEntry) {
				continue
			}
			if cs.Side == Short && dr.InDiscount(cs.BestEntry) {
				continue
			}
		}

		entry, sl, tp := cs.BestEntry, cs.BestSL, cs.BestTP
		if p.ATRMult > 0 && p.ATRPeriod > 0 {
			atr := ATRAt(candles, i, p.ATRPeriod)
			if atr > 0 {
				mult := p.ATRMult * sessionATRScale(c.Time)
				structuralD := math.Abs(entry - sl)
				atrD := atr * mult
				if atrD > structuralD {
					if cs.Side == Long {
						sl = entry - atrD
						tp = entry + atrD*strategy.DefaultRR
					} else {
						sl = entry + atrD
						tp = entry - atrD*strategy.DefaultRR
					}
				}
			}
		}
		if p.MaxSLPoints > 0 && math.Abs(entry-sl) > p.MaxSLPoints {
			continue
		}
		if cs.Side == Long {
			entry += p.SpreadDollar
		} else {
			entry -= p.SpreadDollar
		}
		open = &Trade{
			Strategy:  "Composite",
			Side:      cs.Side,
			EntryIdx:  i,
			EntryTime: c.Time,
			Entry:     entry,
			SL:        sl,
			TP:        tp,
		}
	}
	return trades
}

func summarizeWFAWindow(w *WFAWindow) {
	wins := 0
	for _, t := range w.OOSTrades {
		if t.Win {
			wins++
		}
		w.OOSNetR += t.R
	}
	if len(w.OOSTrades) > 0 {
		w.OOSWinRate = float64(wins) / float64(len(w.OOSTrades))
	}
	if len(w.OOSTrades) >= 2 {
		rs := make([]float64, len(w.OOSTrades))
		for i, t := range w.OOSTrades {
			rs[i] = t.R
		}
		// Annualise using this window's own trade density so windows of unequal
		// length compare fairly. Fall back to 252 (a daily-trader default) only
		// if the trades degenerate to zero elapsed time.
		span := w.OOSTrades[len(w.OOSTrades)-1].ExitTime.Sub(w.OOSTrades[0].EntryTime).Hours() / 24
		tradesPerYear := 252.0
		if span >= 1 {
			tradesPerYear = float64(len(w.OOSTrades)) * 365.0 / span
		}
		w.OOSSharpe = SharpeRatio(rs, tradesPerYear)
	}
}

// combinedStats rebuilds the running balance over the concatenated OOS trades
// and feeds the result through computeStats so every published metric (Sharpe,
// Calmar, MaxDD, AnnRet…) is computed against the unified curve, not stitched
// together from per-window stats.
func combinedStats(trades []Trade, p BacktestParams) BacktestStats {
	balance := p.StartBalance
	peak := balance
	maxDD := 0.0
	for i := range trades {
		balance *= 1 + (trades[i].R * p.RiskPct)
		trades[i].BalanceAfter = balance
		if balance > peak {
			peak = balance
		}
		if peak > 0 {
			dd := (peak - balance) / peak
			if dd > maxDD {
				maxDD = dd
			}
		}
	}
	return computeStats(trades, p, maxDD, balance)
}
