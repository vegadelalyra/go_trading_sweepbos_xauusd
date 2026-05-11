package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/NimbleMarkets/ntcharts/canvas"
	"github.com/NimbleMarkets/ntcharts/canvas/graph"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/strategy"
)

const (
	Symbol          = "XAU/USD"
	IntervalLabel   = "15m"
	LiveOutputSize  = 100
	HistoricSize    = 500
	LiveTickSeconds = 60
)

type viewMode int

const (
	modeLive viewMode = iota
	modeHistoric
	modeBacktest
)

func (v viewMode) String() string {
	switch v {
	case modeLive:
		return "LIVE"
	case modeHistoric:
		return "HISTORIC"
	case modeBacktest:
		return "BACKTEST"
	}
	return "?"
}

type tickMsg time.Time

type candlesMsg struct {
	mode    viewMode
	candles []Candle
	cached  bool
}

type errMsg struct{ err error }

type backtestMsg struct{ result BacktestResult }

var (
	bullStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#22c55e"))
	bearStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#ef4444"))
	winMarker   = lipgloss.NewStyle().Foreground(lipgloss.Color("#22c55e")).Bold(true)
	lossMarker  = lipgloss.NewStyle().Foreground(lipgloss.Color("#ef4444")).Bold(true)
	entryUp     = lipgloss.NewStyle().Foreground(lipgloss.Color("#fbbf24")).Bold(true)
	entryDown   = lipgloss.NewStyle().Foreground(lipgloss.Color("#fbbf24")).Bold(true)
	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFD700")).
			Background(lipgloss.Color("#1a1a1a")).
			Padding(0, 1)
	footerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			Padding(0, 1)
	chartBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("63"))
	statsBorder = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("99")).
			Padding(0, 1)
	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ef4444")).
			Bold(true)
	modeLiveStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#22c55e")).Bold(true)
	modeHistStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#3b82f6")).Bold(true)
	modeBTStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#a855f7")).Bold(true)
	axisStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

type model struct {
	apiKey    string
	httpc     *http.Client
	pool      *pgxpool.Pool
	width     int
	height    int
	mode      viewMode
	candles   []Candle
	lastPrice float64
	credits   int
	loading   bool
	err       error
	result    *BacktestResult
	running   bool
	status    string
	statsView bool
}

func newModel(apiKey string, pool *pgxpool.Pool) model {
	return model{
		apiKey:  apiKey,
		httpc:   &http.Client{Timeout: 30 * time.Second},
		pool:    pool,
		mode:    modeLive,
		width:   100,
		height:  30,
		loading: true,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(fetchLiveCmd(m.httpc, m.apiKey, m.mode), tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Duration(LiveTickSeconds)*time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func fetchLiveCmd(httpc *http.Client, apiKey string, md viewMode) tea.Cmd {
	return func() tea.Msg {
		size := LiveOutputSize
		if md == modeHistoric {
			size = HistoricSize
		}
		out, err := fetchCandles(httpc, apiKey, size)
		if err != nil {
			return errMsg{err}
		}
		return candlesMsg{mode: md, candles: out}
	}
}

func loadBacktestDataCmd(pool *pgxpool.Pool) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := loadCandles(ctx, pool, Symbol, IntervalLabel, time.Time{}, time.Time{})
		if err != nil {
			return errMsg{err}
		}
		if len(out) == 0 {
			return errMsg{fmt.Errorf("DB has no candles for %s %s — run `backfill` first", Symbol, IntervalLabel)}
		}
		return candlesMsg{mode: modeBacktest, candles: out, cached: true}
	}
}

func runBacktestCmd(candles []Candle) tea.Cmd {
	return func() tea.Msg {
		r := RunBacktest(candles, strategy.All(), DefaultParams())
		return backtestMsg{result: r}
	}
}

func creditCost(md viewMode) int {
	switch md {
	case modeHistoric:
		return 5
	case modeBacktest:
		return 50
	}
	return 1
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "l", "L":
			if m.mode != modeLive {
				m.mode = modeLive
				m.loading = true
				m.result = nil
				return m, fetchLiveCmd(m.httpc, m.apiKey, m.mode)
			}
		case "h", "H":
			if m.mode != modeHistoric {
				m.mode = modeHistoric
				m.loading = true
				m.result = nil
				return m, fetchLiveCmd(m.httpc, m.apiKey, m.mode)
			}
		case "t", "T":
			if m.mode != modeBacktest {
				m.mode = modeBacktest
				m.loading = true
				m.result = nil
				m.status = "loading data…"
				return m, loadBacktestDataCmd(m.pool)
			}
		case "b", "B":
			if m.mode == modeBacktest && len(m.candles) > 0 && !m.running {
				m.running = true
				m.status = "running backtest…"
				return m, runBacktestCmd(m.candles)
			}
		case "r", "R":
			m.loading = true
			m.result = nil
			if m.mode == modeBacktest {
				m.status = "reloading from DB…"
				return m, loadBacktestDataCmd(m.pool)
			}
			return m, fetchLiveCmd(m.httpc, m.apiKey, m.mode)
		case "s", "S":
			if m.mode == modeBacktest && m.result != nil {
				m.statsView = !m.statsView
			}
		case "c", "C":
			m.statsView = false
		}

	case candlesMsg:
		if msg.mode == m.mode {
			m.candles = msg.candles
			if n := len(msg.candles); n > 0 {
				m.lastPrice = msg.candles[n-1].Close
			}
			if !msg.cached {
				m.credits += creditCost(msg.mode)
			}
			m.err = nil
			m.loading = false
			if msg.cached {
				m.status = fmt.Sprintf("loaded %d cached candles", len(msg.candles))
			} else {
				m.status = fmt.Sprintf("fetched %d candles", len(msg.candles))
			}
		}

	case errMsg:
		m.err = msg.err
		m.loading = false
		m.running = false

	case backtestMsg:
		m.result = &msg.result
		m.running = false
		m.status = "done"

	case tickMsg:
		cmds := []tea.Cmd{tickCmd()}
		if m.mode == modeLive {
			cmds = append(cmds, fetchLiveCmd(m.httpc, m.apiKey, m.mode))
		}
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

func (m model) View() string {
	header := m.renderHeader()
	footer := m.renderFooter()

	innerH := m.height - lipgloss.Height(header) - lipgloss.Height(footer) - 2
	innerW := m.width - 2
	if innerH < 6 {
		innerH = 6
	}
	if innerW < 20 {
		innerW = 20
	}

	if m.mode == modeBacktest && m.result != nil {
		if m.statsView {
			body := statsBorder.Render(m.renderRichStats(innerW, innerH))
			return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
		}
		statsBlock := statsBorder.Render(m.renderStats(innerW))
		statsH := lipgloss.Height(statsBlock)
		chartH := innerH - statsH
		if chartH < 6 {
			chartH = 6
		}
		body := chartBorder.Render(m.renderChart(innerW, chartH))
		return lipgloss.JoinVertical(lipgloss.Left, header, body, statsBlock, footer)
	}

	body := chartBorder.Render(m.renderChart(innerW, innerH))
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m model) renderRichStats(w, h int) string {
	if m.result == nil {
		return ""
	}
	full := buildBacktestReport(Symbol, IntervalLabel, *m.result)
	lines := strings.Split(full, "\n")
	if len(lines) > h {
		lines = lines[:h]
	}
	for i, ln := range lines {
		if len(ln) > w {
			lines[i] = ln[:w]
		}
	}
	return strings.Join(lines, "\n")
}

func (m model) renderHeader() string {
	var modeLabel string
	switch m.mode {
	case modeLive:
		modeLabel = modeLiveStyle.Render("LIVE")
	case modeHistoric:
		modeLabel = modeHistStyle.Render("HISTORIC")
	case modeBacktest:
		modeLabel = modeBTStyle.Render("BACKTEST")
	}
	if m.loading || m.running {
		modeLabel += " ..."
	}

	priceLabel := fmt.Sprintf("$%.2f", m.lastPrice)
	if m.lastPrice == 0 {
		priceLabel = "$----"
	}

	line := fmt.Sprintf("XAU/USD | %s | %s | Price: %s | Credits: %d | Candles: %d",
		IntervalLabel, modeLabel, priceLabel, m.credits, len(m.candles))
	if m.status != "" {
		line += " | " + m.status
	}
	if m.err != nil {
		line += "  " + errorStyle.Render("ERR: "+m.err.Error())
	}
	return headerStyle.Width(m.width).Render(line)
}

func (m model) renderFooter() string {
	keys := "L:Live  H:Historic  T:Backtest  B:RunBT  S:Stats  C:Chart  R:Reload  Q:Quit"
	return footerStyle.Width(m.width).Render(keys)
}

func (m model) renderChart(w, h int) string {
	cv := canvas.New(w, h)

	if len(m.candles) == 0 {
		msg := "Loading market data..."
		if m.err != nil {
			msg = "No data — " + m.err.Error()
		}
		cv.SetString(canvas.Point{X: 1, Y: h / 2}, msg)
		return cv.View()
	}

	// Determine visible window of candles.
	candles := m.candles
	startIdx := 0
	if len(candles) > w {
		startIdx = len(candles) - w
		candles = candles[startIdx:]
	}
	startX := w - len(candles)

	pmin, pmax := candles[0].Low, candles[0].High
	for _, c := range candles {
		if c.Low < pmin {
			pmin = c.Low
		}
		if c.High > pmax {
			pmax = c.High
		}
	}
	pad := (pmax - pmin) * 0.05
	if pad == 0 {
		pad = 1
	}
	pmin -= pad
	pmax += pad

	rng := pmax - pmin
	rows := float64(h - 1)
	scaleRows := func(p float64) float64 {
		v := (p - pmin) / rng * rows
		if v < 0 {
			v = 0
		} else if v > rows {
			v = rows
		}
		return v
	}
	priceY := func(p float64) int {
		v := (p - pmin) / rng * rows
		y := int(math.Round(rows - v))
		if y < 0 {
			y = 0
		}
		if y > h-1 {
			y = h - 1
		}
		return y
	}

	bottomY := h - 1
	for i, c := range candles {
		style := bullStyle
		if c.Close < c.Open {
			style = bearStyle
		}
		bl := math.Min(c.Open, c.Close)
		bh := math.Max(c.Open, c.Close)
		graph.DrawCandlestickBottomToTop(
			&cv,
			canvas.Point{X: startX + i, Y: bottomY},
			scaleRows(c.Low),
			scaleRows(bl),
			scaleRows(bh),
			scaleRows(c.High),
			style,
		)
	}

	// Trade markers (Backtest mode).
	if m.mode == modeBacktest && m.result != nil {
		for _, t := range m.result.Trades {
			if t.EntryIdx < startIdx || t.EntryIdx >= startIdx+len(candles) {
				continue
			}
			x := startX + (t.EntryIdx - startIdx)
			cdl := candles[t.EntryIdx-startIdx]
			if t.Side == Long {
				y := priceY(cdl.Low) + 1
				if y > h-1 {
					y = h - 1
				}
				cv.SetRuneWithStyle(canvas.Point{X: x, Y: y}, '▲', entryUp)
			} else {
				y := priceY(cdl.High) - 1
				if y < 0 {
					y = 0
				}
				cv.SetRuneWithStyle(canvas.Point{X: x, Y: y}, '▼', entryDown)
			}

			if t.ExitIdx >= startIdx && t.ExitIdx < startIdx+len(candles) {
				ex := startX + (t.ExitIdx - startIdx)
				ey := priceY(t.ExitPrice)
				st := winMarker
				if !t.Win {
					st = lossMarker
				}
				cv.SetRuneWithStyle(canvas.Point{X: ex, Y: ey}, '●', st)
			}
		}
	}

	cv.SetStringWithStyle(canvas.Point{X: 0, Y: 0}, fmt.Sprintf("%.2f", pmax), axisStyle)
	cv.SetStringWithStyle(canvas.Point{X: 0, Y: h - 1}, fmt.Sprintf("%.2f", pmin), axisStyle)
	return cv.View()
}

func (m model) renderStats(w int) string {
	if m.result == nil {
		return ""
	}
	s := m.result.Stats
	var sb strings.Builder

	pf := fmt.Sprintf("%.2f", s.ProfitFactor)
	if math.IsInf(s.ProfitFactor, 1) {
		pf = "∞"
	}

	line1 := fmt.Sprintf(
		"Trades: %d  ·  Win: %d  ·  Loss: %d  ·  WinRate: %.1f%%  ·  NetR: %+.2f  ·  AvgR: %+.2f  ·  PF: %s",
		s.Total, s.Wins, s.Losses, s.WinRate*100, s.NetR, s.AvgR, pf,
	)
	line2 := fmt.Sprintf(
		"Balance: $%.0f → $%.0f (%+.2f%%)  ·  MaxDD: %.2f%%  ·  Risk/trade: 1%%  ·  Spread: $%.2f",
		s.StartBalance, s.EndBalance,
		(s.EndBalance-s.StartBalance)/s.StartBalance*100,
		s.MaxDDPct*100, m.result.Params.SpreadDollar,
	)
	sb.WriteString(line1)
	sb.WriteString("\n")
	sb.WriteString(line2)

	if len(s.PerStrategy) > 0 {
		names := make([]string, 0, len(s.PerStrategy))
		for k := range s.PerStrategy {
			names = append(names, k)
		}
		sort.Strings(names)
		sb.WriteString("\n")
		for _, n := range names {
			ps := s.PerStrategy[n]
			sb.WriteString(fmt.Sprintf(
				"  %-13s  trades=%-3d  win=%-3d  loss=%-3d  winRate=%5.1f%%  netR=%+5.2f\n",
				n, ps.Total, ps.Wins, ps.Losses, ps.WinRate*100, ps.NetR,
			))
		}
	}

	return strings.TrimRight(sb.String(), "\n")
}
