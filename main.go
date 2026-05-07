package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

// ==========================================
// ⚙️ CONFIGURACIÓN GLOBAL
// ==========================================

const (
	Symbol      = "XAU/USD"
	Spread      = 0.45
	RewardRatio = 2.1
)

type SignalType int

const (
	SignalShort SignalType = iota
	SignalLong
)

type Candle struct {
	Time                   time.Time
	Open, High, Low, Close float64
}

type Signal struct {
	Detected      bool
	Type          SignalType
	Level, Sweep  float64
	Entry, SL, TP float64
	Score         int
	Timestamp     time.Time
}

type Trade struct {
	Type    SignalType
	Win     bool
	ResultR float64
	Score   int
}

func main() {
	isBacktest := flag.Bool("backtest", false, "Ejecuta el simulador histórico con Twelve Data")
	flag.Parse()

	_ = godotenv.Load()
	apiKey := os.Getenv("TWELVEDATA_API_KEY")
	if apiKey == "" {
		log.Fatal("❌ ERROR: Define TWELVEDATA_API_KEY en .env")
	}

	if *isBacktest {
		runBacktest(apiKey)
	} else {
		runLiveMode(apiKey)
	}
}

// ==========================================
// 🔴 MODO LIVE
// ==========================================

func runLiveMode(apiKey string) {
	fmt.Printf("🟢 Bot LIVE Activo [%s] (Twelve Data)\n", Symbol)
	for {
		now := time.Now().UTC()
		if isMarketClosed(now) {
			fmt.Printf("😴 [%s] Mercado Cerrado. Esperando...\n", now.Format("15:04"))
			time.Sleep(1 * time.Hour)
			continue
		}

		// Sincronizar con el cierre de vela de 15m
		nextTick := now.Truncate(15 * time.Minute).Add(15 * time.Minute)
		fmt.Printf("⏳ Esperando cierre de vela a las %s...\n", nextTick.Format("15:04"))
		time.Sleep(time.Until(nextTick) + (10 * time.Second))

		m15, errM := fetchTwelveCandles(apiKey, "15min", 100)
		h1, errH := fetchTwelveCandles(apiKey, "1h", 50)

		if errM == nil && errH == nil && len(m15) > 40 {
			sigS, res := detectBearish(m15, isBearishHTF(h1))
			sigL, sup := detectBullish(m15, isBullishHTF(h1))

			if sigS.Detected {
				printSignal(sigS, res)
			}
			if sigL.Detected {
				printSignal(sigL, sup)
			}
		} else {
			fmt.Println("⚠️ Error al obtener datos o insuficientes velas. Reintentando...")
		}
	}
}

// ==========================================
// 🔵 MODO BACKTEST (Twelve Data)
// ==========================================

func runBacktest(apiKey string) {
	fmt.Printf("📂 Cargando histórico de Twelve Data para %s...\n", Symbol)

	m15, _ := fetchTwelveCandles(apiKey, "15min", 500)
	h1, _ := fetchTwelveCandles(apiKey, "1h", 200)

	if len(m15) < 100 {
		log.Fatal("❌ No hay suficientes datos para el backtest.")
	}

	fmt.Printf("✅ Datos cargados: %d velas. Iniciando simulación...\n", len(m15))

	var allTrades []Trade
	for i := 40; i < len(m15)-50; i++ {
		currentM15 := m15[:i+1]
		ctxH1 := getH1ContextAt(h1, m15[i].Time)

		sigS, _ := detectBearish(currentM15, isBearishHTF(ctxH1))
		if sigS.Detected {
			allTrades = append(allTrades, simulateTrade(m15, sigS, i+1))
			i += 8 // Pequeño cooldown
			continue
		}

		sigL, _ := detectBullish(currentM15, isBullishHTF(ctxH1))
		if sigL.Detected {
			allTrades = append(allTrades, simulateTrade(m15, sigL, i+1))
			i += 8
		}
	}

	analyzeResults(allTrades)
}

// ==========================================
// 📉 LÓGICA TÉCNICA (SMC / Sweep + BOS)
// ==========================================

func detectBearish(candles []Candle, htfAlign bool) (Signal, float64) {
	if len(candles) < 40 { return Signal{}, 0 }
	recent := candles[len(candles)-40:]
	resIdxRel, resPrice := findMajorResistance(recent)
	if resPrice == 0 { return Signal{}, 0 }
	resIdx := (len(candles) - 40) + resIdxRel

	var sweepHigh, hlLevel float64
	foundSweep := false
	score := 1 

	for i := resIdx; i < len(candles); i++ {
		if !foundSweep {
			if candles[i].High > resPrice {
				sweepHigh = candles[i].High
				foundSweep = true
			}
			continue
		}
		if candles[i].High > sweepHigh { sweepHigh = candles[i].High }
		if isSwingLow(candles, i-1) && hlLevel == 0 { hlLevel = candles[i-1].Low }

		if hlLevel > 0 && candles[i].Close < hlLevel {
			if htfAlign { score += 2 }
			if (sweepHigh - resPrice) > 0.2 { score += 1 }

			entry := hlLevel - Spread
			sl := sweepHigh + Spread
			tp := entry - (math.Abs(sl-entry) * RewardRatio)

			return Signal{
				Detected: true, Type: SignalShort, Entry: entry, SL: sl, TP: tp,
				Score: score, Timestamp: candles[i].Time, Sweep: sweepHigh, Level: resPrice,
			}, resPrice
		}
	}
	return Signal{}, 0
}

func detectBullish(candles []Candle, htfAlign bool) (Signal, float64) {
	if len(candles) < 40 { return Signal{}, 0 }
	recent := candles[len(candles)-40:]
	supIdxRel, supPrice := findMajorSupport(recent)
	if supPrice == 0 { return Signal{}, 0 }
	supIdx := (len(candles) - 40) + supIdxRel

	var sweepLow, lhLevel float64
	foundSweep := false
	score := 1

	for i := supIdx; i < len(candles); i++ {
		if !foundSweep {
			if candles[i].Low < supPrice {
				sweepLow = candles[i].Low
				foundSweep = true
			}
			continue
		}
		if candles[i].Low < sweepLow { sweepLow = candles[i].Low }
		if isSwingHigh(candles, i-1) && lhLevel == 0 { lhLevel = candles[i-1].High }

		if lhLevel > 0 && candles[i].Close > lhLevel {
			if htfAlign { score += 2 }
			if (supPrice - sweepLow) > 0.2 { score += 1 }

			entry := lhLevel + Spread
			sl := sweepLow - Spread
			tp := entry + (math.Abs(entry-sl) * RewardRatio)

			return Signal{
				Detected: true, Type: SignalLong, Entry: entry, SL: sl, TP: tp,
				Score: score, Timestamp: candles[i].Time, Sweep: sweepLow, Level: supPrice,
			}, supPrice
		}
	}
	return Signal{}, 0
}

func printSignal(s Signal, keyLevel float64) {
	tipo := "COMPRA (LONG)"; emoji := "🚀"
	if s.Type == SignalShort { tipo = "VENTA (SHORT)"; emoji = "🔥" }
	fmt.Printf("\n%s [%s] SEÑAL DETECTADA\n", emoji, s.Timestamp.Format("15:04:05"))
	fmt.Printf("🚨 PATRÓN: %s | ⭐ SCORE: %d/4\n", tipo, s.Score)
	fmt.Printf("📍 ENTRADA: %.2f | SL: %.2f | TP: %.2f\n", s.Entry, s.SL, s.TP)
	fmt.Println("---------------------------------------------------------")
}

// ==========================================
// 🔌 DATA FETCHERS
// ==========================================

func fetchTwelveCandles(key, interval string, size int) ([]Candle, error) {
	url := fmt.Sprintf("https://api.twelvedata.com/time_series?symbol=%s&interval=%s&outputsize=%d&apikey=%s", Symbol, interval, size, key)
	resp, err := http.Get(url)
	if err != nil { return nil, err }
	defer resp.Body.Close()

	var data struct {
		Values []struct {
			Datetime               string `json:"datetime"`
			Open, High, Low, Close string
		} `json:"values"`
		Status string `json:"status"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil { return nil, err }
	if data.Status == "error" { return nil, fmt.Errorf("TwelveData Error") }

	var res []Candle
	for _, v := range data.Values {
		o, _ := strconv.ParseFloat(v.Open, 64)
		h, _ := strconv.ParseFloat(v.High, 64)
		l, _ := strconv.ParseFloat(v.Low, 64)
		c, _ := strconv.ParseFloat(v.Close, 64)
		t, _ := time.Parse("2006-01-02 15:04:05", v.Datetime)
		res = append(res, Candle{t, o, h, l, c})
	}

	// Orden cronológico (antiguo a reciente)
	for i, j := 0, len(res)-1; i < j; i, j = i+1, j-1 {
		res[i], res[j] = res[j], res[i]
	}
	return res, nil
}

// ==========================================
// 🛠️ HELPERS
// ==========================================

func isMarketClosed(t time.Time) bool {
	w := t.Weekday()
	if w == time.Saturday { return true }
	if w == time.Friday && t.Hour() >= 21 { return true }
	if w == time.Sunday && t.Hour() < 22 { return true }
	return false
}

func getH1ContextAt(h1Data []Candle, targetTime time.Time) []Candle {
	var context []Candle
	for _, c := range h1Data {
		if c.Time.Before(targetTime) || c.Time.Equal(targetTime) {
			context = append(context, c)
		}
	}
	if len(context) > 20 { return context[len(context)-20:] }
	return context
}

func simulateTrade(history []Candle, sig Signal, start int) Trade {
	for i := start; i < len(history); i++ {
		c := history[i]
		if sig.Type == SignalShort {
			if c.High >= sig.SL { return Trade{sig.Type, false, -1.0, sig.Score} }
			if c.Low <= sig.TP { return Trade{sig.Type, true, RewardRatio, sig.Score} }
		} else {
			if c.Low <= sig.SL { return Trade{sig.Type, false, -1.0, sig.Score} }
			if c.High >= sig.TP { return Trade{sig.Type, true, RewardRatio, sig.Score} }
		}
	}
	return Trade{ResultR: 0}
}

func findMajorResistance(c []Candle) (int, float64) {
	h, idx := 0.0, -1
	for i := 2; i < len(c)-2; i++ {
		if c[i].High > c[i-1].High && c[i].High > c[i+1].High && c[i].High > c[i-2].High && c[i].High > c[i+2].High {
			if c[i].High > h { h, idx = c[i].High, i }
		}
	}
	return idx, h
}

func findMajorSupport(c []Candle) (int, float64) {
	l, idx := 999999.0, -1
	for i := 2; i < len(c)-2; i++ {
		if c[i].Low < c[i-1].Low && c[i].Low < c[i+1].Low && c[i].Low < c[i-2].Low && c[i].Low < c[i+2].Low {
			if c[i].Low < l { l, idx = c[i].Low, i }
		}
	}
	return idx, l
}

func isSwingLow(c []Candle, i int) bool {
	if i < 1 || i >= len(c)-1 { return false }
	return c[i].Low < c[i-1].Low && c[i].Low < c[i+1].Low
}

func isSwingHigh(c []Candle, i int) bool {
	if i < 1 || i >= len(c)-1 { return false }
	return c[i].High > c[i-1].High && c[i].High > c[i+1].High
}

func isBearishHTF(c []Candle) bool {
	if len(c) < 2 { return false }
	return c[len(c)-1].Close < c[len(c)-2].Low
}

func isBullishHTF(c []Candle) bool {
	if len(c) < 2 { return false }
	return c[len(c)-1].Close > c[len(c)-2].High
}

func analyzeResults(trades []Trade) {
	fmt.Println("\n📊 --- RESULTADOS DEL BACKTEST ---")
	metrics := make(map[int]struct{ w, l int; r float64 })
	for _, t := range trades {
		if t.ResultR == 0 { continue }
		m := metrics[t.Score]
		if t.Win { m.w++; m.r += t.ResultR } else { m.l++; m.r += t.ResultR }
		metrics[t.Score] = m
	}
	fmt.Printf("%-10s | %-8s | %-8s | %-8s\n", "Score Min", "Trades", "Winrate", "Net R")
	for s := 1; s <= 4; s++ {
		var tw, tl int; var tr float64
		for score, data := range metrics {
			if score >= s { tw += data.w; tl += data.l; tr += data.r }
		}
		total := tw + tl
		if total > 0 {
			fmt.Printf(">= %-7d | %-8d | %-7.1f%% | %-8.2f\n", s, total, (float64(tw)/float64(total))*100, tr)
		}
	}
}