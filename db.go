package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func jsonEncoder(v any) ([]byte, error) { return json.Marshal(v) }

const defaultDSN = "postgres://emy_admin:emy-1234@127.0.0.1:5432/candles?sslmode=disable"

func openPool(ctx context.Context) (*pgxpool.Pool, error) {
	dsn := strings.TrimSpace(os.Getenv("CANDLES_DSN"))
	if dsn == "" {
		dsn = defaultDSN
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 8
	cfg.MinConns = 1
	cfg.MaxConnLifetime = 30 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

// upsertCandles inserts (or ignores) candles for (symbol, interval).
// Returns the number of rows actually inserted (excluding conflicts).
func upsertCandles(ctx context.Context, pool *pgxpool.Pool, symbol, interval string, candles []Candle) (int, error) {
	if len(candles) == 0 {
		return 0, nil
	}
	rows := make([][]any, 0, len(candles))
	for _, c := range candles {
		rows = append(rows, []any{symbol, interval, c.Time.UTC(), c.Open, c.High, c.Low, c.Close})
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `CREATE TEMP TABLE _ohlc_stage (
		symbol TEXT, interval TEXT, bar_time TIMESTAMPTZ,
		open DOUBLE PRECISION, high DOUBLE PRECISION,
		low DOUBLE PRECISION, close DOUBLE PRECISION
	) ON COMMIT DROP`); err != nil {
		return 0, fmt.Errorf("create stage: %w", err)
	}

	_, err = tx.CopyFrom(ctx, pgx.Identifier{"_ohlc_stage"},
		[]string{"symbol", "interval", "bar_time", "open", "high", "low", "close"},
		pgx.CopyFromRows(rows))
	if err != nil {
		return 0, fmt.Errorf("copy: %w", err)
	}

	ct, err := tx.Exec(ctx, `
		INSERT INTO ohlc (symbol, interval, bar_time, open, high, low, close)
		SELECT symbol, interval, bar_time, open, high, low, close FROM _ohlc_stage
		ON CONFLICT (symbol, interval, bar_time) DO NOTHING
	`)
	if err != nil {
		return 0, fmt.Errorf("merge: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return int(ct.RowsAffected()), nil
}

// loadCandles returns candles for (symbol, interval) within the optional time range.
// Use zero time.Time as "open-ended" on either side.
func loadCandles(ctx context.Context, pool *pgxpool.Pool, symbol, interval string, from, to time.Time) ([]Candle, error) {
	q := `SELECT bar_time, open, high, low, close FROM ohlc
	      WHERE symbol = $1 AND interval = $2`
	args := []any{symbol, interval}
	idx := 3
	if !from.IsZero() {
		q += fmt.Sprintf(" AND bar_time >= $%d", idx)
		args = append(args, from.UTC())
		idx++
	}
	if !to.IsZero() {
		q += fmt.Sprintf(" AND bar_time <= $%d", idx)
		args = append(args, to.UTC())
		idx++
	}
	q += " ORDER BY bar_time ASC"

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]Candle, 0, 1024)
	for rows.Next() {
		var c Candle
		if err := rows.Scan(&c.Time, &c.Open, &c.High, &c.Low, &c.Close); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type CoverageRow struct {
	Interval string
	Count    int
	Oldest   time.Time
	Newest   time.Time
}

// saveBacktestRun persists a backtest result: one row in backtest_runs and
// one row per executed trade in backtest_trades. Returns the new run id.
func saveBacktestRun(ctx context.Context, pool *pgxpool.Pool, symbol, interval string, candles []Candle, r BacktestResult) (int64, error) {
	if len(candles) == 0 {
		return 0, fmt.Errorf("no candles in result")
	}
	paramsJSON, err := jsonMarshal(r.Params)
	if err != nil {
		return 0, fmt.Errorf("marshal params: %w", err)
	}
	statsJSON, err := jsonMarshal(r.Stats)
	if err != nil {
		return 0, fmt.Errorf("marshal stats: %w", err)
	}

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var runID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO backtest_runs (symbol, interval, started_at, ended_at, candle_count, params, stats)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`,
		symbol, interval,
		candles[0].Time.UTC(),
		candles[len(candles)-1].Time.UTC(),
		len(candles),
		paramsJSON, statsJSON,
	).Scan(&runID)
	if err != nil {
		return 0, fmt.Errorf("insert run: %w", err)
	}

	if len(r.Trades) > 0 {
		rows := make([][]any, 0, len(r.Trades))
		for _, t := range r.Trades {
			rows = append(rows, []any{
				runID, t.Strategy, t.Side.String(),
				t.EntryIdx, t.ExitIdx,
				t.EntryTime.UTC(), t.ExitTime.UTC(),
				t.Entry, t.SL, t.TP, t.ExitPrice,
				t.R, t.Win, t.CandlesToExit,
				t.Session, t.YearMonth, t.YearQuarter,
				t.BalanceAfter,
			})
		}
		_, err = tx.CopyFrom(ctx, pgx.Identifier{"backtest_trades"},
			[]string{
				"run_id", "strategy", "side",
				"entry_idx", "exit_idx",
				"entry_time", "exit_time",
				"entry_price", "sl", "tp", "exit_price",
				"r_realized", "win", "candles_to_exit",
				"session", "year_month", "year_quarter",
				"balance_after",
			}, pgx.CopyFromRows(rows))
		if err != nil {
			return 0, fmt.Errorf("copy trades: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return runID, nil
}

func jsonMarshal(v any) ([]byte, error) {
	return jsonEncoder(v)
}

// coverage reports how many candles we already have per interval for a symbol.
func coverage(ctx context.Context, pool *pgxpool.Pool, symbol string) ([]CoverageRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT interval, COUNT(*), MIN(bar_time), MAX(bar_time)
		FROM ohlc WHERE symbol = $1
		GROUP BY interval ORDER BY interval`, symbol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CoverageRow
	for rows.Next() {
		var cr CoverageRow
		var oldest, newest *time.Time
		if err := rows.Scan(&cr.Interval, &cr.Count, &oldest, &newest); err != nil {
			return nil, err
		}
		if oldest != nil {
			cr.Oldest = *oldest
		}
		if newest != nil {
			cr.Newest = *newest
		}
		out = append(out, cr)
	}
	return out, rows.Err()
}
