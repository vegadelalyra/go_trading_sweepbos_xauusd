-- Schema inicial para la DB `candles`.
-- Aplicar con:
--   PGPASSWORD=emy-1234 psql -h 127.0.0.1 -p 5432 -U emy_admin -d candles -f schema/001_init.sql

BEGIN;

-- =====================================================================
-- ohlc: velas crudas, multi-symbol, multi-interval, deduplicadas.
-- Clave compuesta (symbol, interval, bar_time) garantiza idempotencia
-- en el backfill paginado: re-fetch de un rango no duplica filas.
-- =====================================================================
CREATE TABLE IF NOT EXISTS ohlc (
    symbol     TEXT             NOT NULL,
    interval   TEXT             NOT NULL,
    bar_time   TIMESTAMPTZ      NOT NULL,
    open       DOUBLE PRECISION NOT NULL,
    high       DOUBLE PRECISION NOT NULL,
    low        DOUBLE PRECISION NOT NULL,
    close      DOUBLE PRECISION NOT NULL,
    volume     DOUBLE PRECISION,
    source     TEXT             NOT NULL DEFAULT 'twelvedata',
    fetched_at TIMESTAMPTZ      NOT NULL DEFAULT now(),
    PRIMARY KEY (symbol, interval, bar_time)
);

CREATE INDEX IF NOT EXISTS idx_ohlc_query
    ON ohlc (symbol, interval, bar_time DESC);

-- =====================================================================
-- backtest_runs: una fila por corrida de backtest, con sus parámetros
-- y stats agregadas para comparar evoluciones del motor o de las
-- estrategias en el tiempo.
-- =====================================================================
CREATE TABLE IF NOT EXISTS backtest_runs (
    id           BIGSERIAL    PRIMARY KEY,
    symbol       TEXT         NOT NULL,
    interval     TEXT         NOT NULL,
    started_at   TIMESTAMPTZ  NOT NULL,
    ended_at     TIMESTAMPTZ  NOT NULL,
    candle_count INTEGER      NOT NULL,
    params       JSONB        NOT NULL,
    stats        JSONB        NOT NULL,
    git_sha      TEXT,
    notes        TEXT,
    created_at   TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_runs_recent
    ON backtest_runs (symbol, interval, created_at DESC);

-- =====================================================================
-- backtest_trades: cada trade ejecutado en un run.
-- Las columnas session / year_month / year_quarter están
-- pre-calculadas para hacer GROUP BY rápido por bucket sin
-- recomputar zonas horarias en SQL.
-- =====================================================================
CREATE TABLE IF NOT EXISTS backtest_trades (
    id              BIGSERIAL        PRIMARY KEY,
    run_id          BIGINT           NOT NULL REFERENCES backtest_runs(id) ON DELETE CASCADE,
    strategy        TEXT             NOT NULL,
    side            TEXT             NOT NULL CHECK (side IN ('LONG','SHORT')),
    entry_idx       INTEGER          NOT NULL,
    exit_idx        INTEGER          NOT NULL,
    entry_time      TIMESTAMPTZ      NOT NULL,
    exit_time       TIMESTAMPTZ      NOT NULL,
    entry_price     DOUBLE PRECISION NOT NULL,
    sl              DOUBLE PRECISION NOT NULL,
    tp              DOUBLE PRECISION NOT NULL,
    exit_price      DOUBLE PRECISION NOT NULL,
    r_realized      DOUBLE PRECISION NOT NULL,
    win             BOOLEAN          NOT NULL,
    candles_to_exit INTEGER          NOT NULL,
    session         TEXT             NOT NULL,
    year_month      INTEGER          NOT NULL,
    year_quarter    INTEGER          NOT NULL,
    balance_after   DOUBLE PRECISION
);

CREATE INDEX IF NOT EXISTS idx_trades_run_strategy
    ON backtest_trades (run_id, strategy);

CREATE INDEX IF NOT EXISTS idx_trades_session_bucket
    ON backtest_trades (run_id, strategy, session);

CREATE INDEX IF NOT EXISTS idx_trades_month_bucket
    ON backtest_trades (run_id, strategy, year_month);

COMMIT;
