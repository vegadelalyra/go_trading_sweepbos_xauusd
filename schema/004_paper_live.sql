-- Persistencia para paper-live: corre indefinidamente, fetchea la última
-- vela vía REST cada ciclo, identifica señales y las grada contra velas
-- futuras hasta SL/TP/time-stop. Necesita dos cosas que el esquema 003 no
-- tenía: una marca para distinguir runs "live" de runs de replay one-shot,
-- y una tabla de posiciones abiertas (pending) que se vacía a medida que
-- los trades resuelven y migran a paper_trades.
--
-- Aplicar con:
--   PGPASSWORD=emy-1234 psql -h 127.0.0.1 -p 5432 -U emy_admin -d candles -f schema/004_paper_live.sql

BEGIN;

-- Distingue runs auto-running (live) de runs one-shot (replay). El loop
-- de paper-live setea esto = true al arrancar; consultores pueden filtrar
-- por kind sin tener que mirar el ended_at = started_at heurístico.
ALTER TABLE paper_runs
    ADD COLUMN IF NOT EXISTS is_live BOOLEAN NOT NULL DEFAULT false;

-- paper_pending: una fila por posición abierta. Cuando una vela posterior
-- toca SL o TP, o cuando se alcanza MaxHoldCandles, la fila migra a
-- paper_trades (en la misma TX) y se borra de aquí. Si el script crashea,
-- al re-arrancar con -resume estas filas se siguen gradeando.
CREATE TABLE IF NOT EXISTS paper_pending (
    id              BIGSERIAL        PRIMARY KEY,
    paper_run_id    BIGINT           NOT NULL REFERENCES paper_runs(id) ON DELETE CASCADE,
    strategy        TEXT             NOT NULL,
    side            TEXT             NOT NULL CHECK (side IN ('LONG','SHORT')),
    entry_time      TIMESTAMPTZ      NOT NULL,
    entry_price     DOUBLE PRECISION NOT NULL,
    sl              DOUBLE PRECISION NOT NULL,
    tp              DOUBLE PRECISION NOT NULL,
    session         TEXT             NOT NULL,
    year_month      INTEGER          NOT NULL,
    year_quarter    INTEGER          NOT NULL,
    created_at      TIMESTAMPTZ      NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_paper_pending_run
    ON paper_pending (paper_run_id, entry_time);

COMMIT;
