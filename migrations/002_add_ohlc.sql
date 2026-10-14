-- Run as owner/importer before deploying to an existing PostgreSQL database.
-- Preserve existing close-only records: unknown OHLC values remain NULL.
BEGIN;
ALTER TABLE prices
    ADD COLUMN IF NOT EXISTS open_price NUMERIC(16,4) CHECK (open_price >= 0),
    ADD COLUMN IF NOT EXISTS high_price NUMERIC(16,4) CHECK (high_price >= 0),
    ADD COLUMN IF NOT EXISTS low_price NUMERIC(16,4) CHECK (low_price >= 0);
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'prices'::regclass AND conname = 'prices_ohlc_order'
    ) THEN
        ALTER TABLE prices ADD CONSTRAINT prices_ohlc_order CHECK (
            high_price >= low_price AND
            open_price <= high_price AND open_price >= low_price AND
            close_price <= high_price AND close_price >= low_price
        );
    END IF;
END;
$$;
COMMIT;
