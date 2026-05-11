package main

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/engine"
)

// printBacktestReport writes a CLI-friendly multi-bucket breakdown to stdout.
// It shares its rendering logic with the TUI so a single source of truth
// describes every panel.
func printBacktestReport(symbol, interval string, r engine.BacktestResult) {
	fmt.Print(buildBacktestReport(symbol, interval, r))
}

// buildBacktestReport returns the same content as printBacktestReport as a
// single string — used by the TUI stats view.
func buildBacktestReport(symbol, interval string, r engine.BacktestResult) string {
	var b strings.Builder
	s := r.Stats
	fmt.Fprintf(&b, "═══ %s %s — Backtest Report ═══\n", symbol, interval)
	fmt.Fprintln(&b, headerLine(s))

	fmt.Fprintln(&b, "\n── Per Strategy ─────────────────────────────────────────────")
	fmt.Fprintf(&b, "  %-14s  %-6s  %-5s  %-5s  %-8s  %-8s\n",
		"Strategy", "Trades", "Win", "Loss", "WinRate", "NetR")
	for _, name := range sortedKeys(s.PerStrategy) {
		ps := s.PerStrategy[name]
		fmt.Fprintf(&b, "  %-14s  %-6d  %-5d  %-5d  %6.1f%%  %+8.2f\n",
			name, ps.Total, ps.Wins, ps.Losses, ps.WinRate*100, ps.NetR)
	}

	if len(s.BySession) > 0 {
		fmt.Fprintln(&b, "\n── By Session (winrate% / netR / n) ─────────────────────────")
		sessions := []string{SessionAsia, SessionLondon, SessionNYAM, SessionNYPM, SessionOff}
		fmt.Fprintf(&b, "  %-14s", "Strategy")
		for _, sess := range sessions {
			fmt.Fprintf(&b, "  %-16s", sess)
		}
		fmt.Fprintln(&b)
		for _, name := range sortedKeysNested(s.BySession) {
			row := s.BySession[name]
			fmt.Fprintf(&b, "  %-14s", name)
			for _, sess := range sessions {
				st, ok := row[sess]
				if !ok || st.Total == 0 {
					fmt.Fprintf(&b, "  %-16s", "—")
					continue
				}
				cell := fmt.Sprintf("%4.0f%%/%+4.1f/%-3d", st.WinRate*100, st.NetR, st.Total)
				fmt.Fprintf(&b, "  %-16s", cell)
			}
			fmt.Fprintln(&b)
		}
	}

	if len(s.ByMonth) > 0 {
		monthKeys := allBucketKeys(s.ByMonth)
		sort.Strings(monthKeys)
		fmt.Fprintln(&b, "\n── By Month (winrate%) ──────────────────────────────────────")
		fmt.Fprintf(&b, "  %-14s", "Strategy")
		for _, mk := range monthKeys {
			fmt.Fprintf(&b, "  %7s", mk)
		}
		fmt.Fprintln(&b)
		for _, name := range sortedKeysNested(s.ByMonth) {
			row := s.ByMonth[name]
			fmt.Fprintf(&b, "  %-14s", name)
			for _, mk := range monthKeys {
				st, ok := row[mk]
				if !ok || st.Total == 0 {
					fmt.Fprintf(&b, "  %7s", "—")
					continue
				}
				fmt.Fprintf(&b, "  %6.0f%%", st.WinRate*100)
			}
			fmt.Fprintln(&b)
		}
	}

	if len(s.Duration) > 0 {
		fmt.Fprintln(&b, "\n── Duration (candles to exit) ───────────────────────────────")
		for _, name := range sortedStringIntKeys(s.Duration) {
			d := append([]int(nil), s.Duration[name]...)
			if len(d) == 0 {
				continue
			}
			sort.Ints(d)
			fmt.Fprintf(&b, "  %-14s  n=%-3d  p25=%-3d  p50=%-3d  p75=%-3d  p90=%-3d  max=%d\n",
				name, len(d),
				percentileInt(d, 25), percentileInt(d, 50),
				percentileInt(d, 75), percentileInt(d, 90), d[len(d)-1])
		}
	}

	if len(s.RDist) > 0 {
		fmt.Fprintln(&b, "\n── R distribution (per trade) ───────────────────────────────")
		for _, name := range sortedStringFloatKeys(s.RDist) {
			rs := s.RDist[name]
			if len(rs) == 0 {
				continue
			}
			expectancy := mean(rs)
			worst, best := minMax(rs)
			maxLossStreak := longestLossStreak(rs)
			fmt.Fprintf(&b, "  %-14s  n=%-3d  expectancy=%+0.3fR  worst=%+0.2fR  best=%+0.2fR  maxLossStreak=%d\n",
				name, len(rs), expectancy, worst, best, maxLossStreak)
		}
	}
	return b.String()
}

func headerLine(s engine.BacktestStats) string {
	pf := fmt.Sprintf("%.2f", s.ProfitFactor)
	if math.IsInf(s.ProfitFactor, 1) {
		pf = "∞"
	}
	pct := 0.0
	if s.StartBalance > 0 {
		pct = (s.EndBalance - s.StartBalance) / s.StartBalance * 100
	}
	main := fmt.Sprintf(
		"Trades=%d  Win=%d Loss=%d  WR=%.1f%%  NetR=%+.2f  AvgR=%+.3f  PF=%s  MaxDD=%.1f%%  $%.0f → $%.0f (%+.2f%%)",
		s.Total, s.Wins, s.Losses, s.WinRate*100, s.NetR, s.AvgR, pf, s.MaxDDPct*100,
		s.StartBalance, s.EndBalance, pct,
	)
	if s.DaysCovered > 0 {
		main += fmt.Sprintf(
			"\nSharpe=%.2f  Calmar=%.2f  Recovery=%.2f  AnnRet=%.1f%%  Streak(W/L)=%d/%d  Days=%d",
			s.Sharpe, s.Calmar, s.RecoveryFactor, s.AnnualReturnPct,
			s.MaxWinStreak, s.MaxLossStreak, s.DaysCovered,
		)
	}
	return main
}

// ── helpers ─────────────────────────────────────────────────────────────────

func sortedKeys(m map[string]engine.StrategyStats) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysNested(m map[string]map[string]engine.StrategyStats) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedStringIntKeys(m map[string][]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedStringFloatKeys(m map[string][]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func allBucketKeys(m map[string]map[string]engine.StrategyStats) []string {
	seen := map[string]bool{}
	for _, inner := range m {
		for k := range inner {
			seen[k] = true
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

func percentileInt(sorted []int, p int) int {
	if len(sorted) == 0 {
		return 0
	}
	idx := (p * (len(sorted) - 1)) / 100
	return sorted[idx]
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range xs {
		sum += v
	}
	return sum / float64(len(xs))
}

func minMax(xs []float64) (float64, float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	mn, mx := xs[0], xs[0]
	for _, v := range xs[1:] {
		if v < mn {
			mn = v
		}
		if v > mx {
			mx = v
		}
	}
	return mn, mx
}

func longestLossStreak(rs []float64) int {
	best, cur := 0, 0
	for _, r := range rs {
		if r < 0 {
			cur++
			if cur > best {
				best = cur
			}
		} else {
			cur = 0
		}
	}
	return best
}

// trim removes whitespace from prefixed strings used in formatting.
func trim(s string) string { return strings.TrimSpace(s) }

var _ = trim // keep imported "strings" alive even if helpers change later
