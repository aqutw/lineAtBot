-- Usage: psql "$DATABASE_URL" -f migrations/001_postgres.sql -f scripts/import_postgres.sql
-- Use the owner/importer connection, not the read-only bot connection.
-- These CSVs are SIMULATED fixtures. Replace them with your own data before importing.
BEGIN;
CREATE TEMP TABLE import_stocks (code TEXT, name TEXT);
CREATE TEMP TABLE import_prices (stock_code TEXT, trading_date DATE, close_price NUMERIC(16,4));
\copy import_stocks FROM 'data/stocks.csv' WITH (FORMAT csv, HEADER true)
\copy import_prices FROM 'data/prices.csv' WITH (FORMAT csv, HEADER true)
INSERT INTO stocks SELECT * FROM import_stocks ON CONFLICT(code) DO UPDATE SET name=excluded.name;
INSERT INTO prices SELECT * FROM import_prices ON CONFLICT(stock_code,trading_date)
DO UPDATE SET close_price=excluded.close_price;
COMMIT;
