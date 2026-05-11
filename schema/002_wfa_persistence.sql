-- Persistencia de runs de Walk-Forward Analysis y sus trades OOS.
-- Aplicar con:
--   PGPASSWORD=emy-1234 psql -h 127.0.0.1 -p 5432 -U emy_admin -d candles -f schema/002_wfa_persistence.sql
--
-- Motivación: el bootstrap hasta ahora opera sobre backtest_trades, que
-- pertenecen a runs plain (sin pesos ni filtros). El IC honesto del sistema
-- vive en la curva OOS concatenada del WFA. Estas dos tablas la materializan
-- en DB para que el bootstrap pueda apuntarse a un wfa_runs.id y medir el
-- intervalo de confianza sobre la curva real, no sobre un run sin gobierno.

BEGIN;

-- =====================================================================
-- wfa_runs: una fila por corrida WFA con sus parámetros y stats agregadas
-- sobre la curva OOS combinada. params/stats se guardan en JSONB para
-- evolucionar sin tocar el schema.
-- =====================================================================
CREATE TABLE IF NOT EXISTS wfa_runs (
    id                    BIGSERIAL    PRIMARY KEY,
    symbol                TEXT         NOT NULL,
    interval              TEXT         NOT NULL,
    started_at            TIMESTAMPTZ  NOT NULL,
    ended_at              TIMESTAMPTZ  NOT NULL,
    is_months             INTEGER      NOT NULL,
    oos_months            INTEGER      NOT NULL,
    step_months           INTEGER      NOT NULL,
    threshold             DOUBLE PRECISION NOT NULL,
    use_bias              BOOLEAN      NOT NULL,
    atr_mult              DOUBLE PRECISION NOT NULL,
    pd_lookback           INTEGER      NOT NULL,
    slippage_points       DOUBLE PRECISION NOT NULL,
    slippage_news_mult    DOUBLE PRECISION NOT NULL,
    h4_period             INTEGER      NOT NULL,
    windows_count         INTEGER      NOT NULL,
    stats                 JSONB        NOT NULL,
    created_at            TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_wfa_runs_recent
    ON wfa_runs (symbol, interval, created_at DESC);

-- =====================================================================
-- wfa_trades: cada trade OOS dentro de una ventana del WFA. window_idx
-- identifica de qué ventana (1-based) salió el trade, lo cual permite
-- bootstrap o análisis por ventana sin reconstruir cronologías.
-- Mismo esquema columnar que backtest_trades para que los consumidores
-- (bootstrap, reports) puedan compartir la misma struct in-memory.
-- =====================================================================
CREATE TABLE IF NOT EXISTS wfa_trades (
    id              BIGSERIAL        PRIMARY KEY,
    wfa_run_id      BIGINT           NOT NULL REFERENCES wfa_runs(id) ON DELETE CASCADE,
    window_idx      INTEGER          NOT NULL,
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

CREATE INDEX IF NOT EXISTS idx_wfa_trades_run_time
    ON wfa_trades (wfa_run_id, entry_time ASC);

CREATE INDEX IF NOT EXISTS idx_wfa_trades_run_window
    ON wfa_trades (wfa_run_id, window_idx);

COMMIT;
