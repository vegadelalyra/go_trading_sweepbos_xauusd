package livefeed

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/vegadelalyra/go_trading_sweepbos_xauusd/internal/core"
)

// REST fetching of closed candles via Twelve Data's free `time_series`
// endpoint — the WSS stream above is paid-tier only, and paper-live needs
// to keep accumulating candles for free. This file deliberately mirrors
// the same TD response shape as data.go's fetchPage in package main so the
// two paths produce identical Candle structs; the small duplication keeps
// internal/livefeed self-contained and importable from any internal pkg.

const (
	restEndpoint = "https://api.twelvedata.com/time_series"
	restDateFmt  = "2006-01-02 15:04:05"
)

type tdResponse struct {
	Status string `json:"status"`
	Msg    string `json:"message,omitempty"`
	Values []struct {
		Datetime string `json:"datetime"`
		Open     string `json:"open"`
		High     string `json:"high"`
		Low      string `json:"low"`
		Close    string `json:"close"`
	} `json:"values"`
}

// FetchLatest returns the n most-recent candles for (symbol, interval) in
// chronological order (oldest first). Caller is expected to upsert these
// into the ohlc table; FetchLatest itself is stateless.
//
// On the free tier this is the only way to keep the candle store current.
// Rate-limited to a few calls per minute by Twelve Data; paper-live should
// space them at the bar-close cadence (one per 15m bar, ~96/day) rather
// than polling on a tight sub-minute loop.
func FetchLatest(ctx context.Context, httpc *http.Client, apiKey, symbol, interval string, n int) ([]core.Candle, error) {
	q := url.Values{}
	q.Set("symbol", symbol)
	q.Set("interval", restInterval(interval))
	q.Set("outputsize", strconv.Itoa(n))
	q.Set("apikey", apiKey)
	q.Set("timezone", "UTC")
	endpoint := restEndpoint + "?" + q.Encode()

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

	nv := len(data.Values)
	out := make([]core.Candle, 0, nv)
	// TD returns newest-first; flip to oldest-first to match the rest of
	// the engine's contract on candle ordering.
	for i := nv - 1; i >= 0; i-- {
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
		t, err := time.Parse(restDateFmt, v.Datetime)
		if err != nil {
			continue
		}
		out = append(out, core.Candle{Time: t, Open: o, High: h, Low: l, Close: c})
	}
	return out, nil
}

func restInterval(interval string) string {
	switch interval {
	case "5m":
		return "5min"
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
