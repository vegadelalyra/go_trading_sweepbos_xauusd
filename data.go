package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
)

const (
	tdEndpoint = "https://api.twelvedata.com/time_series"
	dateFormat = "2006-01-02 15:04:05"
)

type tdResponse struct {
	Status string `json:"status"`
	Code   int    `json:"code,omitempty"`
	Msg    string `json:"message,omitempty"`
	Values []struct {
		Datetime string `json:"datetime"`
		Open     string `json:"open"`
		High     string `json:"high"`
		Low      string `json:"low"`
		Close    string `json:"close"`
	} `json:"values"`
}

// tdInterval converts internal interval labels ("15m","1h","4h") to TwelveData's.
func tdInterval(interval string) string {
	switch interval {
	case "15m":
		return "15min"
	case "1h":
		return "1h"
	case "4h":
		return "4h"
	case "1d":
		return "1day"
	default:
		return interval
	}
}

// fetchPage retrieves up to outputSize candles for (symbol, interval).
// If endDate is zero, retrieves the most recent candles. Otherwise retrieves
// candles ending at endDate (exclusive upper bound from TD's perspective).
// Returns candles in chronological order (oldest first).
func fetchPage(ctx context.Context, httpc *http.Client, apiKey, symbol, interval string, endDate time.Time, outputSize int) ([]core.Candle, error) {
	q := url.Values{}
	q.Set("symbol", symbol)
	q.Set("interval", tdInterval(interval))
	q.Set("outputsize", strconv.Itoa(outputSize))
	q.Set("apikey", apiKey)
	q.Set("timezone", "UTC")
	if !endDate.IsZero() {
		q.Set("end_date", endDate.UTC().Format(dateFormat))
	}
	endpoint := tdEndpoint + "?" + q.Encode()

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var data tdResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if data.Status != "ok" {
		if data.Msg != "" {
			return nil, fmt.Errorf("api: %s", data.Msg)
		}
		return nil, fmt.Errorf("api status=%s", data.Status)
	}

	n := len(data.Values)
	out := make([]core.Candle, 0, n)
	// TD returns newest-first; reverse to oldest-first.
	for i := n - 1; i >= 0; i-- {
		v := data.Values[i]
		o, errO := strconv.ParseFloat(v.Open, 64)
		h, errH := strconv.ParseFloat(v.High, 64)
		l, errL := strconv.ParseFloat(v.Low, 64)
		c, errC := strconv.ParseFloat(v.Close, 64)
		if errO != nil || errH != nil || errL != nil || errC != nil {
			continue
		}
		if o == 0 || h == 0 || l == 0 || c == 0 {
			continue
		}
		t, err := time.Parse(dateFormat, v.Datetime)
		if err != nil {
			continue
		}
		out = append(out, core.Candle{Time: t, Open: o, High: h, Low: l, Close: c})
	}
	return out, nil
}

// fetchCandles is kept as the legacy helper used by the live/historic modes
// in the TUI. It defaults to the XAU/USD 15m feed.
func fetchCandles(httpc *http.Client, apiKey string, size int) ([]core.Candle, error) {
	return fetchPage(context.Background(), httpc, apiKey, Symbol, "15m", time.Time{}, size)
}

// loadCacheJSON / saveCacheJSON are retained so we can one-shot import the
// legacy JSON dump that pre-dated the Postgres schema. New code should not
// depend on these.
func loadCacheJSON(path string) ([]core.Candle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []core.Candle
	if err := json.NewDecoder(f).Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
