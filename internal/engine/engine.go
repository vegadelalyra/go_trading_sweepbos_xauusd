// Package engine owns the backtest simulator: parameters, statistics
// accumulation and the candle-by-candle execution loop. It depends on
// core (Candle, Trade, Side, session helpers), smc (Analyze, dealing-range
// premium/discount), indicator (ATR), bias (H4 filter) and the strategy
// catalog — but not on any caller (cli, wfa, tui, db), so any consumer
// can replay the exact engine that produced a stored run.
package engine

import (
	"fmt"
	"math"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/bias"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/indicator"
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
	H4Bias    bias.BiasMap // if non-empty, reject signals whose Side disagrees with the H4 EMA200 bias at entry time
	ATRPeriod int          // candles in ATR window (e.g. 14)
	ATRMult   float64      // if >0, widen structural SL to max(structural, ATR × ATRMult)
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
	// TripleBarrier — optional partial-TP / breakeven / dynamic-time-stop
	// exit overlay (López de Prado triple-barrier method). Disabled by
	// default; when enabled, transforms the SL/TP-only lifecycle into
	// partial-leg-at-+PartialR + breakeven-on-remainder + time-stop-on-
	// stagnation. See TripleBarrierConfig for the per-knob semantics.
	TripleBarrier TripleBarrierConfig
	// PerStrategyRR, when non-nil, overrides the TP that each emitted
	// signal carries: if PerStrategyRR[sig.Strategy] > 0 the TP is
	// recomputed via strategy.TPFromRR using the supplied RR (the SL
	// stays as the strategy decided, only the asymmetry changes). Used
	// by WFA's Bayesian shrinkage path to let high-volume strategies
	// trade their own data-driven RR while sparse ones revert to the
	// global prior. Default nil = legacy behaviour, every strategy
	// keeps its baked-in RR.
	PerStrategyRR map[string]float64
	// BarDuration is the wall-clock duration of one OHLC candle (5min,
	// 15min, 1h, 4h). Set by ParamsFor; zero falls back to 5min for
	// internal helpers that need to walk N bars backward in time (e.g.,
	// the spread-spike filter's trailing SMA).
	BarDuration time.Duration
	// SpreadCurve, when Enabled, replaces the constant SpreadDollar with
	// a time-of-day synthetic curve (rollover blow-out, news-window
	// widening) and optionally rejects signals during spread-spike bars.
	// Default zero-value (Enabled=false) preserves bit-for-bit legacy
	// behaviour — the dynamic curve only kicks in when the caller opts in.
	SpreadCurve SpreadCurveConfig
}

// TripleBarrierConfig parametrises the triple-barrier exit overlay. All
// thresholds zero-value to a no-op (Enabled=false), so existing callers
// stay reproducible.
type TripleBarrierConfig struct {
	// Enabled toggles the overlay. False → legacy lifecycle (one SL, one
	// TP, optional MaxHoldCandles); true → the three barriers below.
	Enabled bool
	// PartialR is the R-multiple at which the partial-TP leg fires. The
	// half-size partial closes 50% of the position at +PartialR, and the
	// SL on the remaining 50% moves to entry (breakeven). Default 1.5R.
	// Setting this <= 0 with Enabled=true is invalid (no-op).
	PartialR float64
	// TimeStopCandles is the bar count after which the dynamic time-stop
	// becomes eligible. Default 8 (40min on 5m). Setting to 0 disables
	// this barrier independently of the others.
	TimeStopCandles int
	// TimeStopRangeR defines the |unrealised R| band that classifies the
	// position as "stagnating" once TimeStopCandles is reached. Inside
	// the band → close at market; outside → keep running. Default 0.3R.
	TimeStopRangeR float64
}

// DefaultTripleBarrierConfig returns the canonical thresholds (PartialR=1.5,
// TimeStopCandles=8 ≈ 40min on 5m, TimeStopRangeR=0.3) with Enabled=false.
// Callers that want the overlay must flip Enabled=true explicitly.
func DefaultTripleBarrierConfig() TripleBarrierConfig {
	return TripleBarrierConfig{
		Enabled:         false,
		PartialR:        1.5,
		TimeStopCandles: 8,
		TimeStopRangeR:  0.3,
	}
}

func DefaultParams() BacktestParams {
	return BacktestParams{
		StartBalance:    100_000,
		RiskPct:         0.01,
		DailyDDLimit:    0.05,
		SpreadDollar:    0.50,
		MinIndex:        50,
		MaxSLPoints:     50,
		MaxHoldCandles:  0, // no time-stop; mark-to-market at end-of-data instead
		CooldownCandles: 4,
	}
}

// ParamsFor15m / ParamsFor1h / ParamsFor4h tune the per-timeframe caps so the
// same DefaultParams template still works at every interval the engine accepts.
//
// MaxHoldCandles is 0 by default: trades run until SL or TP touches,
// with a final mark-to-market close-out on the last available bar so
// open positions don't silently disappear when the backtest window
// ends. The earlier 6h/12h/24h caps were a defensive heuristic ("SMC
// setups should mature within a session"); removing them lets winners
// reach full +RR and losers reach full -1R. Net effect on edge is an
// open empirical question — see project memory for the latest sweep.
// Callers that want a time-stop can override MaxHoldCandles per run.
func ParamsFor(interval string) BacktestParams {
	p := DefaultParams()
	switch interval {
	case "5m":
		// XAU/USD 5m typical 1-3 dollar moves → tighter MaxSLPoints than
		// 15m. Cooldown stays 4 candles (20min) which is short enough that
		// the strategy can re-enter on the next setup but long enough to
		// avoid re-firing on the same chop.
		p.MaxSLPoints = 30
		p.MaxHoldCandles = 0
		p.BarDuration = 5 * time.Minute
	case "15m":
		p.MaxSLPoints = 50
		p.MaxHoldCandles = 0
		p.BarDuration = 15 * time.Minute
	case "1h":
		p.MaxSLPoints = 100
		p.MaxHoldCandles = 0
		p.BarDuration = time.Hour
	case "4h":
		p.MaxSLPoints = 200
		p.MaxHoldCandles = 0
		p.BarDuration = 4 * time.Hour
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
	Sharpe          float64
	RecoveryFactor  float64
	Calmar          float64
	AnnualReturnPct float64
	MaxLossStreak   int
	MaxWinStreak    int
	DaysCovered     int
}

type StrategyStats struct {
	Total   int
	Wins    int
	Losses  int
	NetR    float64
	WinRate float64
}

type BacktestResult struct {
	Trades []core.Trade
	Stats  BacktestStats
	Params BacktestParams
}

// RunBacktest evaluates `strategies` against `candles` using the supplied
// BacktestParams. Pass strategy.All() to use every registered strategy
// (production case); pass a curated subset to test a single strategy in
// isolation. Order matters — the engine fires the first match per candle.
func RunBacktest(candles []core.Candle, strategies []strategy.Strategy, p BacktestParams) BacktestResult {
	a := smc.Analyze(candles)

	balance := p.StartBalance
	peak := balance
	maxDDPct := 0.0

	dayStart := balance
	currentDay := -1

	var trades []core.Trade
	var open *core.Trade
	cooldown := 0

	for i := p.MinIndex; i < len(candles); i++ {
		c := candles[i]
		ny := core.ToNY(c.Time)
		yd := ny.YearDay()
		if yd != currentDay {
			currentDay = yd
			dayStart = balance
		}

		// daily drawdown halt: skip new entries this day
		dailyHalt := dayStart > 0 && (dayStart-balance)/dayStart >= p.DailyDDLimit

		if open != nil {
			if AdvanceTrade(open, c, i, p) {
				risk := balance * p.RiskPct
				balance += risk * open.R
				open.BalanceAfter = balance
				open.Session = core.ClassifySession(open.EntryTime)
				open.YearMonth = core.YearMonthBucket(open.EntryTime)
				open.YearQuarter = core.YearQuarterBucket(open.EntryTime)
				open.CandlesToExit = open.ExitIdx - open.EntryIdx
				trades = append(trades, *open)
				open = nil
				cooldown = p.CooldownCandles

				if balance > peak {
					peak = balance
				}
				if peak > 0 {
					if dd := (peak - balance) / peak; dd > maxDDPct {
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
			if b := p.H4Bias.Lookup(candles[i].Time); b != 0 {
				if b > 0 && sig.Side == core.Short {
					continue
				}
				if b < 0 && sig.Side == core.Long {
					continue
				}
			}
			// F6 — Optional ATR-based SL widening: keep the larger of the
			// strategy's structural SL and ATR × session-adjusted-mult.
			// Asia is structurally tighter (smaller true ranges) so we
			// shrink the multiplier there; NY-PM and the dead "off" band
			// also drift quieter. London and NY-AM keep the full multiplier.
			if p.ATRMult > 0 && p.ATRPeriod > 0 {
				atr := indicator.ATRAt(candles, i, p.ATRPeriod)
				if atr > 0 {
					mult := p.ATRMult * SessionATRScale(c.Time)
					structuralD := math.Abs(sig.Entry - sig.SL)
					atrD := atr * mult
					if atrD > structuralD {
						if sig.Side == core.Long {
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
			//
			// Breakout strategies are EXEMPT — they fire at the range
			// extreme by construction, so the PD filter would silence
			// them entirely. See strategy.IsBreakout for the list.
			if p.PDLookback > 0 && !strategy.IsBreakout(sig.Strategy) {
				dr := smc.CurrentDealingRange(candles, i, p.PDLookback)
				if sig.Side == core.Long && dr.InPremium(sig.Entry) {
					continue
				}
				if sig.Side == core.Short && dr.InDiscount(sig.Entry) {
					continue
				}
			}
			// Per-strategy RR override (Bayesian shrinkage, Phase 2.1).
			// When the caller supplies a PerStrategyRR map, the strategy's
			// baked-in TP is recomputed using the shrunk RR — same SL
			// anchor, different asymmetry. Sparse strategies in the map
			// land on the global prior, dense ones on their own MLE.
			// Applied AFTER ATR widening so the shrinkage is the final
			// word on TP. Strategies absent from the map keep their
			// intrinsic TP.
			if rr, ok := p.PerStrategyRR[sig.Strategy]; ok && rr > 0 {
				sig.TP = strategy.TPFromRR(sig.Entry, sig.SL, rr, sig.Side == core.Long)
			}
			// Engine-level guard: reject signals whose SL distance is unreasonable.
			if p.MaxSLPoints > 0 {
				slDist := math.Abs(sig.Entry - sig.SL)
				if slDist > p.MaxSLPoints {
					continue
				}
			}
			// Spread-spike filter (Phase 4.2 stub on the synthetic curve;
			// will gate against real broker telemetry once paper-live
			// records bid/ask via FIX). Reject pre-entry when modelled
			// spread is more than FilterMult × trailing SMA — the engine
			// pretends to be a careful trader who walks away from
			// rollover gaps and news-print blow-outs.
			barDur := p.BarDuration
			if barDur <= 0 {
				barDur = 5 * time.Minute
			}
			if p.SpreadCurve.SpreadSpike(c.Time, barDur) {
				continue
			}
			spread := p.SpreadCurve.EffectiveSpread(c.Time, p.SpreadDollar)
			if sig.Side == core.Long {
				sig.Entry += spread
			} else {
				sig.Entry -= spread
			}
			t := core.Trade{
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

	// End-of-data mark-to-market. Without MaxHoldCandles enforcing a
	// time-stop, a position still open at the last bar would silently
	// drop from the trade record. Close it at the last candle's close
	// using the same R formula the time-stop branch used, so stats and
	// equity curve include every trade that opened during the run.
	if open != nil && len(candles) > 0 {
		last := candles[len(candles)-1]
		open.ExitPrice = last.Close
		if open.Side == core.Long {
			rd := open.Entry - open.SL
			if rd > 0 {
				open.R = (last.Close - open.Entry) / rd
			}
		} else {
			rd := open.SL - open.Entry
			if rd > 0 {
				open.R = (open.Entry - last.Close) / rd
			}
		}
		open.Win = open.R > 0
		open.ExitIdx = len(candles) - 1
		open.ExitTime = last.Time
		risk := balance * p.RiskPct
		balance += risk * open.R
		open.BalanceAfter = balance
		open.Session = core.ClassifySession(open.EntryTime)
		open.YearMonth = core.YearMonthBucket(open.EntryTime)
		open.YearQuarter = core.YearQuarterBucket(open.EntryTime)
		open.CandlesToExit = open.ExitIdx - open.EntryIdx
		trades = append(trades, *open)
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

	return BacktestResult{
		Trades: trades,
		Stats:  ComputeStats(trades, p, maxDDPct, balance),
		Params: p,
	}
}

// ComputeStats aggregates a trade slice into the headline + bucket stats
// used by every reporter. Exported because wfa.combinedStats needs to call
// it on the concatenated OOS curve.
func ComputeStats(trades []core.Trade, p BacktestParams, maxDDPct, endBal float64) BacktestStats {
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

func bumpStats(m map[string]StrategyStats, key string, t core.Trade) {
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

// AdvanceTrade processes one candle for an open position and reports
// whether the position fully resolved at this bar. Single source of truth
// for the intra-position lifecycle — engine.RunBacktest, wfa.SimulateOOS
// and paper.gradeOne all delegate here so the legacy and triple-barrier
// branches stay in sync across the three replay sites.
//
// Semantics:
//   - When TripleBarrier is disabled (default), the function reduces to
//     the legacy lifecycle: SL → -1R (or slipped), TP → +RR, optional
//     MaxHoldCandles → mark-to-market at close.
//   - When TripleBarrier.Enabled is true:
//     1. If the partial-TP barrier hasn't fired yet (open.PartialR == 0)
//        and the bar reaches +PartialR, the partial leg closes 50% at
//        that level and the SL on the remaining 50% moves to entry
//        (breakeven). Position keeps running.
//     2. SL/TP checks then run against the (possibly moved) SL.
//     3. After TimeStopCandles bars, if |unrealised R| ≤ TimeStopRangeR,
//        the remainder closes at market (dynamic time-stop).
//     4. Legacy MaxHoldCandles still fires last as a hard cap.
//
// On close, open.R carries the weighted average across the partial and
// final legs (0.5·PartialR + 0.5·finalLegR when the partial fired; just
// finalLegR otherwise). open.ExitPrice / open.ExitIdx / open.ExitTime
// are set on the FINAL leg's exit. Callers update balance and the
// bucket fields (Session / YearMonth / etc.).
//
// Single-bar ordering convention is favorable to the trader: partial
// fires before any same-bar SL touch, and TP fires before SL on the
// final leg. This matches the existing engine's convention.
func AdvanceTrade(open *core.Trade, c core.Candle, i int, p BacktestParams) bool {
	slip := SLPenalty(p, c.Time)
	spread := p.SpreadCurve.EffectiveSpread(c.Time, p.SpreadDollar)
	tb := p.TripleBarrier
	tbActive := tb.Enabled && tb.PartialR > 0

	// ── Phase 1: partial-TP barrier (only if enabled and not yet fired).
	if tbActive && open.PartialR == 0 {
		riskDist := math.Abs(open.Entry - open.SL)
		if riskDist > 0 {
			if open.Side == core.Long {
				target := open.Entry + tb.PartialR*riskDist
				if c.High >= target {
					fill := target - spread
					open.PartialR = (fill - open.Entry) / riskDist
					open.PartialExitPrice = fill
					open.PartialExitIdx = i
					open.PartialExitTime = c.Time
					open.SL = open.Entry // breakeven on remaining half
				}
			} else {
				target := open.Entry - tb.PartialR*riskDist
				if c.Low <= target {
					fill := target + spread
					open.PartialR = (open.Entry - fill) / riskDist
					open.PartialExitPrice = fill
					open.PartialExitIdx = i
					open.PartialExitTime = c.Time
					open.SL = open.Entry // breakeven on remaining half
				}
			}
		}
	}

	// finalize closes the trade with the supplied final-leg R, combining
	// with PartialR (when present) to produce the weighted Trade.R.
	finalize := func(exitPrice, finalR float64, win bool) {
		open.ExitPrice = exitPrice
		open.ExitIdx = i
		open.ExitTime = c.Time
		if open.PartialR != 0 {
			open.R = 0.5*open.PartialR + 0.5*finalR
		} else {
			open.R = finalR
		}
		open.Win = open.R > 0
		_ = win
	}

	// ── Phase 2: SL / TP on the (possibly BE-moved) SL.
	if open.Side == core.Long {
		if c.Low <= open.SL {
			riskDist := open.Entry - open.SL
			if open.PartialR != 0 {
				// SL has been moved to entry (BE) — clean BE exit, no
				// slippage modelled on the BE itself because there is no
				// stop-hunting penalty when the stop is AT the entry.
				finalize(open.SL-spread, 0, false)
				return true
			}
			if slip > 0 {
				exit := open.SL - slip
				if c.Low < exit {
					exit = c.Low
				}
				actualLoss := open.Entry - exit + spread
				finalize(exit-spread, -actualLoss/riskDist, false)
			} else {
				finalize(open.SL-spread, -1, false)
			}
			return true
		}
		if c.High >= open.TP {
			riskDist := math.Abs(open.Entry - origSL(open, tbActive))
			finalLegR := (open.TP - open.Entry) / riskDist
			finalize(open.TP-spread, finalLegR, true)
			return true
		}
	} else {
		if c.High >= open.SL {
			riskDist := open.SL - open.Entry
			if open.PartialR != 0 {
				finalize(open.SL+spread, 0, false)
				return true
			}
			if slip > 0 {
				exit := open.SL + slip
				if c.High > exit {
					exit = c.High
				}
				actualLoss := exit - open.Entry + spread
				finalize(exit+spread, -actualLoss/riskDist, false)
			} else {
				finalize(open.SL+spread, -1, false)
			}
			return true
		}
		if c.Low <= open.TP {
			riskDist := math.Abs(origSL(open, tbActive) - open.Entry)
			finalLegR := (open.Entry - open.TP) / riskDist
			finalize(open.TP+spread, finalLegR, true)
			return true
		}
	}

	// ── Phase 3: dynamic time-stop (triple-barrier overlay).
	if tbActive && tb.TimeStopCandles > 0 && i-open.EntryIdx >= tb.TimeStopCandles {
		riskDist := math.Abs(open.Entry - origSL(open, tbActive))
		if riskDist > 0 {
			var unrealised float64
			if open.Side == core.Long {
				unrealised = (c.Close - open.Entry) / riskDist
			} else {
				unrealised = (open.Entry - c.Close) / riskDist
			}
			if math.Abs(unrealised) <= tb.TimeStopRangeR {
				finalize(c.Close, unrealised, unrealised > 0)
				return true
			}
		}
	}

	// ── Phase 4: legacy MaxHoldCandles hard cap (unchanged).
	if p.MaxHoldCandles > 0 && i-open.EntryIdx >= p.MaxHoldCandles {
		riskDist := math.Abs(open.Entry - origSL(open, tbActive))
		var legR float64
		if riskDist > 0 {
			if open.Side == core.Long {
				legR = (c.Close - open.Entry) / riskDist
			} else {
				legR = (open.Entry - c.Close) / riskDist
			}
		}
		finalize(c.Close, legR, legR > 0)
		return true
	}

	return false
}

// origSL returns the original risk-distance reference for R computations.
// Once the partial leg has fired, open.SL is moved to entry (BE), but
// risk-multiples must remain anchored to the ORIGINAL SL so TP/+RR math
// keeps reporting the strategy's intended R. When the triple-barrier is
// disabled or the partial hasn't fired, open.SL is still the original.
//
// Approximation: we don't carry the original SL explicitly. Instead, when
// the partial fired, we infer the original riskDist from open.PartialR and
// the partial fill price. PartialR_realised = (fill - entry) / riskDist
// (long) so riskDist = (fill - entry) / PartialR_realised. We then return
// entry ± riskDist depending on side. This avoids a new field on
// core.Trade and stays accurate to the partial fill we recorded.
func origSL(open *core.Trade, tbActive bool) float64 {
	if !tbActive || open.PartialR == 0 {
		return open.SL
	}
	if open.Side == core.Long {
		riskDist := (open.PartialExitPrice - open.Entry) / open.PartialR
		return open.Entry - riskDist
	}
	riskDist := (open.Entry - open.PartialExitPrice) / open.PartialR
	return open.Entry + riskDist
}

// SLPenalty returns the dollar slippage applied to an SL-hit exit price for
// candle c. Default (SlippagePoints == 0) preserves the old behaviour of
// filling at the SL exactly. When set, the engine pretends price gapped a
// few cents past the stop — and multiplies the penalty during the news
// windows that produce most gold gap-and-go moves.
func SLPenalty(p BacktestParams, t time.Time) float64 {
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
	h := core.ToNY(t).Hour()
	return h == 8 || h == 9 || h == 10 || h == 14
}

// SessionATRScale returns the multiplier applied to BacktestParams.ATRMult
// based on the candle's session. The numbers come from measuring 15m ATR
// medians per session over the 2y XAU dataset — Asia prints roughly 0.67×
// the London/NY range, NY-PM trails at 0.85×, the dead band at 0.80×.
// Returning 1.0 for the active sessions keeps existing tunings intact.
func SessionATRScale(t time.Time) float64 {
	switch core.ClassifySession(t) {
	case core.SessionAsia:
		return 0.67
	case core.SessionLondon, core.SessionNYAM:
		return 1.0
	case core.SessionNYPM:
		return 0.85
	default:
		return 0.80
	}
}

func bumpNestedStats(m map[string]map[string]StrategyStats, strategy, bucket string, t core.Trade) {
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
