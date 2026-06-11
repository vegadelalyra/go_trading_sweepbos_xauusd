-- 006: marca de "canónico" para backtest_runs, así paper-live puede
-- elegir el último run confiable automáticamente sin que experimentos
-- manuales (`backtest -persist`) contaminen el default.
--
-- Aplicar con:
--   PGPASSWORD=emy-1234 psql -h 127.0.0.1 -p 5432 -U emy_admin -d candles -f schema/006_canonical_flag.sql

BEGIN;

ALTER TABLE backtest_runs
    ADD COLUMN IF NOT EXISTS is_canonical BOOLEAN NOT NULL DEFAULT FALSE;

-- Filtro frecuente: "último canonical para este symbol+interval".
CREATE INDEX IF NOT EXISTS idx_runs_canonical_recent
    ON backtest_runs (symbol, interval, created_at DESC)
    WHERE is_canonical = TRUE;

-- Bootstrap: el run que estabas usando manualmente (id=19) se queda
-- como canonical para no romper la operación corriente. El auto-refresh
-- agregará nuevos canonical runs cada día hábil.
UPDATE backtest_runs SET is_canonical = TRUE WHERE id = 19;

COMMIT;
