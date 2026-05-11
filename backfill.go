package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// BackfillConfig drives a backward-paginated download.
type BackfillConfig struct {
	Symbol      string
	Interval    string
	TargetStart time.Time     // download until at least this date is reached
	PageSize    int           // candles per TD call (TD max = 5000)
	Throttle    time.Duration // sleep between calls to respect rate limit
	MaxPages    int           // safety cap; 0 = unlimited
}

func defaultBackfillCfg(symbol, interval string, years int) BackfillConfig {
	return BackfillConfig{
		Symbol:      symbol,
		Interval:    interval,
		TargetStart: time.Now().UTC().AddDate(-years, 0, 0),
		PageSize:    5000,
		Throttle:    8 * time.Second, // TD free tier = 8 req/min
		MaxPages:    50,
	}
}

// backfill walks TD pages backward from "now" until TargetStart is reached.
// It always starts at end_date=now (so the most recent tail gets refreshed),
// and skips over any contiguous range already cached in the DB so we don't
// burn TD calls on data we already have.
func backfill(ctx context.Context, pool *pgxpool.Pool, httpc *http.Client, apiKey string, cfg BackfillConfig) (int, error) {
	if cfg.PageSize <= 0 {
		cfg.PageSize = 5000
	}
	if cfg.Throttle <= 0 {
		cfg.Throttle = 8 * time.Second
	}

	endDate := time.Time{} // first page = "now"
	totalInserted := 0
	page := 0
	for {
		page++
		if cfg.MaxPages > 0 && page > cfg.MaxPages {
			fmt.Printf("  [%s] hit MaxPages=%d, stopping\n", cfg.Interval, cfg.MaxPages)
			break
		}

		candles, err := fetchPage(ctx, httpc, apiKey, cfg.Symbol, cfg.Interval, endDate, cfg.PageSize)
		if err != nil {
			return totalInserted, fmt.Errorf("page %d: %w", page, err)
		}
		if len(candles) == 0 {
			fmt.Printf("  [%s] empty page, stopping\n", cfg.Interval)
			break
		}

		oldest := candles[0].Time.UTC()
		newest := candles[len(candles)-1].Time.UTC()

		inserted, err := upsertCandles(ctx, pool, cfg.Symbol, cfg.Interval, candles)
		if err != nil {
			return totalInserted, fmt.Errorf("upsert page %d: %w", page, err)
		}
		totalInserted += inserted

		fmt.Printf("  [%s] page %2d  fetched=%4d  inserted=%4d  range=%s → %s\n",
			cfg.Interval, page, len(candles), inserted,
			oldest.Format("2006-01-02 15:04"), newest.Format("2006-01-02 15:04"))

		if !oldest.After(cfg.TargetStart) {
			fmt.Printf("  [%s] reached target %s\n", cfg.Interval, cfg.TargetStart.Format("2006-01-02"))
			break
		}

		// If we hit a page that's entirely cached, jump straight to the
		// oldest bar we have in DB and keep walking from there.
		var nextEnd time.Time
		if inserted == 0 {
			dbOldest, err := dbOldestBar(ctx, pool, cfg.Symbol, cfg.Interval)
			if err != nil {
				return totalInserted, err
			}
			if dbOldest.IsZero() || !dbOldest.Before(oldest) {
				nextEnd = oldest.Add(-time.Second)
			} else {
				fmt.Printf("  [%s] page fully cached, jumping to DB oldest %s\n",
					cfg.Interval, dbOldest.Format("2006-01-02 15:04"))
				nextEnd = dbOldest.Add(-time.Second)
			}
		} else {
			nextEnd = oldest.Add(-time.Second)
		}
		endDate = nextEnd

		select {
		case <-ctx.Done():
			return totalInserted, ctx.Err()
		case <-time.After(cfg.Throttle):
		}
	}
	return totalInserted, nil
}

// dbOldestBar returns the oldest bar_time stored for (symbol, interval).
func dbOldestBar(ctx context.Context, pool *pgxpool.Pool, symbol, interval string) (time.Time, error) {
	var oldest *time.Time
	err := pool.QueryRow(ctx,
		`SELECT MIN(bar_time) FROM ohlc WHERE symbol = $1 AND interval = $2`,
		symbol, interval).Scan(&oldest)
	if err != nil {
		return time.Time{}, err
	}
	if oldest == nil {
		return time.Time{}, nil
	}
	return *oldest, nil
}

