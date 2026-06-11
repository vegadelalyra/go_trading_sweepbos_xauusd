-- Paper-trading persistence: replay the composite signal engine over
-- historical (or future, when live) candles with a FIXED weight set,
-- and record every trade for grading + downstream weight feedback.
--
-- Aplicar con:
--   PGPASSWORD=emy-1234 psql -h 127.0.0.1 -p 5432 -U emy_admin -d candles -f schema/003_paper_signals.sql
--
-- Motivación: WFA OOS responde "¿habrían funcionado las estrategias ayer?"
-- re-entrenando pesos cada ventana. Paper-replay responde una pregunta
-- distinta: "si congelo este set de pesos HOY y los uso N meses, ¿qué pasa?".
-- También es la base para feedback paper→pesos en commit 2: paper_trades
-- comparte el shape de backtest_trades para que LoadWeights pueda UNION-arlas.

BEGIN;

-- =====================================================================
-- paper_runs: header de una corrida de paper-replay con su contexto
-- (qué pesos, qué rango de velas, qué parámetros del motor).
-- =====================================================================
CREATE TABLE IF NOT EXISTS paper_runs (
    id                    BIGSERIAL    PRIMARY KEY,
    source_run_id         BIGINT       NOT NULL REFERENCES backtest_runs(id),
    by_session            BOOLEAN      NOT NULL,
    symbol                TEXT         NOT NULL,
    interval              TEXT         NOT NULL,
    started_at            TIMESTAMPTZ  NOT NULL,
    ended_at              TIMESTAMPTZ  NOT NULL,
    threshold             DOUBLE PRECISION NOT NULL,
    use_bias              BOOLEAN      NOT NULL,
    atr_mult              DOUBLE PRECISION NOT NULL,
    pd_lookback           INTEGER      NOT NULL,
    slippage_points       DOUBLE PRECISION NOT NULL,
    slippage_news_mult    DOUBLE PRECISION NOT NULL,
    h4_period             INTEGER      NOT NULL,
    stats                 JSONB        NOT NULL,
    created_at            TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_paper_runs_recent
    ON paper_runs (symbol, interval, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_paper_runs_source
    ON paper_runs (source_run_id);

-- =====================================================================
-- paper_trades: shape idéntico a backtest_trades para que LoadWeights
-- pueda UNION-arlos sin transformaciones. Diferencia clave: el `strategy`
-- ya viene atribuido al WinnerStrategy de meta.ComposeSignal (la
-- estrategia cuyo entry/SL/TP se tomó en la votación) en vez de un
-- genérico "Composite", para que el feedback de pesos del commit 2 sume
-- al edge de la estrategia real, no a un sumidero composite.
-- =====================================================================
CREATE TABLE IF NOT EXISTS paper_trades (
    id              BIGSERIAL        PRIMARY KEY,
    paper_run_id    BIGINT           NOT NULL REFERENCES paper_runs(id) ON DELETE CASCADE,
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

CREATE INDEX IF NOT EXISTS idx_paper_trades_run_time
    ON paper_trades (paper_run_id, entry_time ASC);

CREATE INDEX IF NOT EXISTS idx_paper_trades_run_strategy
    ON paper_trades (paper_run_id, strategy);

COMMIT;
