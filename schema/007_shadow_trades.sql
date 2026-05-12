-- 007: shadow_trades — contrafactual de señales que acceptSignal rechazó
-- (H4 bias, PD, MaxSL). Cada rechazo abre una posición virtual con el mismo
-- entry/SL/TP que la real habría tenido, se gradea barra a barra via
-- engine.AdvanceTrade y se cierra con SL/TP/MaxHold. Sirve para auditar
-- en frío si los filtros aportan edge (rechazos deberían tener WR < aceptados)
-- o si están dejando R sobre la mesa.
--
-- Nota: el spread NO rechaza en acceptSignal; solo se hornea en entry_price.
-- Si en el futuro se agrega un gate de spread (SpikeFilter pre-vote, etc.),
-- ampliar el CHECK de reject_gate con el nuevo código.
--
-- Diseño single-table con status: filas 'open' viven mientras la posición
-- está in-flight, mutan a 'closed' cuando AdvanceTrade resuelve. Columnas
-- exit_* son NULL mientras status='open'. No es producción; es auditoría
-- asíncrona consumida vía CLI `shadow-stats`, por eso no replicamos el
-- patrón pending+trades de paper-live.
--
-- Aplicar con:
--   PGPASSWORD=emy-1234 psql -h 127.0.0.1 -p 5432 -U emy_admin -d candles -f schema/007_shadow_trades.sql

BEGIN;

CREATE TABLE IF NOT EXISTS shadow_trades (
    id                BIGSERIAL        PRIMARY KEY,
    paper_run_id      BIGINT           NOT NULL REFERENCES paper_runs(id) ON DELETE CASCADE,
    rejecter          TEXT             NOT NULL,
    -- reject_gate identifica qué puerta de acceptSignal disparó:
    -- 'BIAS' (H4 bias), 'PD' (premium/discount), 'MAXSL'.
    -- Permite agregaciones por gate sin parsear la reason de texto libre.
    reject_gate       TEXT             NOT NULL CHECK (reject_gate IN ('BIAS','PD','MAXSL')),
    reject_reason     TEXT             NOT NULL,
    strategy          TEXT             NOT NULL,
    side              TEXT             NOT NULL CHECK (side IN ('LONG','SHORT')),
    entry_time        TIMESTAMPTZ      NOT NULL,
    entry_price       DOUBLE PRECISION NOT NULL,
    sl                DOUBLE PRECISION NOT NULL,
    tp                DOUBLE PRECISION NOT NULL,
    net_weight        DOUBLE PRECISION NOT NULL,
    applied_threshold DOUBLE PRECISION NOT NULL,
    status            TEXT             NOT NULL CHECK (status IN ('open','closed')) DEFAULT 'open',
    -- Exit columns: NULL while status='open', populated atomically on close.
    exit_time         TIMESTAMPTZ,
    exit_price        DOUBLE PRECISION,
    r_realized        DOUBLE PRECISION,
    win               BOOLEAN,
    candles_to_exit   INTEGER,
    session           TEXT             NOT NULL,
    year_month        INTEGER          NOT NULL,
    year_quarter      INTEGER          NOT NULL,
    created_at        TIMESTAMPTZ      NOT NULL DEFAULT now()
);

-- Hot path: cada barra de paper-live lee los 'open' del run actual para
-- avanzarlos via AdvanceTrade. Índice parcial mantiene el escaneo barato
-- aun cuando shadow_trades acumule decenas de miles de filas cerradas.
CREATE INDEX IF NOT EXISTS idx_shadow_trades_open
    ON shadow_trades (paper_run_id, entry_time)
    WHERE status = 'open';

-- Analítico: `shadow-stats --by-rejecter` y `--by-gate` agregan sobre
-- todos los cerrados de un run.
CREATE INDEX IF NOT EXISTS idx_shadow_trades_closed_rejecter
    ON shadow_trades (paper_run_id, rejecter, reject_gate)
    WHERE status = 'closed';

COMMIT;
