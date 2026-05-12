-- Phase 3.2 — COMEX gold options/futures monthly expiry calendar.
-- Drives the meta.COMEXPinConfig pre-expiry pinning overlay: during the
-- 48h before each expiry, dealer gamma-hedging around the highest-OI
-- strikes creates a gravitational pin that crushes momentum breakouts
-- and feeds mean-reversion setups. The composer reads these dates via
-- meta.LoadExpiryDates and silences momentum strategies (×0.0) while
-- boosting mean-reversion strategies (×1.3) inside the window.
--
-- Seed strategy: GC contracts settle on the last business day of the
-- contract month; in practice the last Friday is a close-enough proxy
-- for the 48h pin window (typically Fri close to Sun-Mon settlement
-- mechanics on the CME side). The 26 rows below cover 2024-04 → 2026-05,
-- which matches the WFA OOS window. Pre-seeded inline rather than
-- generated at app startup so the calendar is auditable in SQL and a
-- mis-build doesn't silently degrade the overlay.
--
-- Aplicar con:
--   PGPASSWORD=emy-1234 psql -h 127.0.0.1 -p 5432 -U emy_admin -d candles -f schema/005_comex_expiry.sql

BEGIN;

CREATE TABLE IF NOT EXISTS comex_expiry (
    expiry_date DATE PRIMARY KEY,
    contract    TEXT NOT NULL DEFAULT 'GC',
    notes       TEXT
);

-- Last-Friday-of-month expiries, 2024-04 through 2026-05.
-- Idempotent: re-running this migration is a no-op thanks to ON CONFLICT.
INSERT INTO comex_expiry (expiry_date, contract, notes) VALUES
    ('2024-04-26', 'GC', 'last Fri of month'),
    ('2024-05-31', 'GC', 'last Fri of month'),
    ('2024-06-28', 'GC', 'last Fri of month'),
    ('2024-07-26', 'GC', 'last Fri of month'),
    ('2024-08-30', 'GC', 'last Fri of month'),
    ('2024-09-27', 'GC', 'last Fri of month'),
    ('2024-10-25', 'GC', 'last Fri of month'),
    ('2024-11-29', 'GC', 'last Fri of month'),
    ('2024-12-27', 'GC', 'last Fri of month'),
    ('2025-01-31', 'GC', 'last Fri of month'),
    ('2025-02-28', 'GC', 'last Fri of month'),
    ('2025-03-28', 'GC', 'last Fri of month'),
    ('2025-04-25', 'GC', 'last Fri of month'),
    ('2025-05-30', 'GC', 'last Fri of month'),
    ('2025-06-27', 'GC', 'last Fri of month'),
    ('2025-07-25', 'GC', 'last Fri of month'),
    ('2025-08-29', 'GC', 'last Fri of month'),
    ('2025-09-26', 'GC', 'last Fri of month'),
    ('2025-10-31', 'GC', 'last Fri of month'),
    ('2025-11-28', 'GC', 'last Fri of month'),
    ('2025-12-26', 'GC', 'last Fri of month'),
    ('2026-01-30', 'GC', 'last Fri of month'),
    ('2026-02-27', 'GC', 'last Fri of month'),
    ('2026-03-27', 'GC', 'last Fri of month'),
    ('2026-04-24', 'GC', 'last Fri of month'),
    ('2026-05-29', 'GC', 'last Fri of month')
ON CONFLICT (expiry_date) DO NOTHING;

COMMIT;
