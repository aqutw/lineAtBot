-- Usage: psql "$DATABASE_URL" -v ON_ERROR_STOP=1 -f migrations/001_postgres.sql -f migrations/002_add_ohlc.sql -f scripts/import_postgres.sql
-- Use the owner/importer connection, not the read-only bot connection.
-- These CSVs are SIMULATED fixtures. Replace them with your own data before importing.
BEGIN;
CREATE TEMP TABLE import_stocks (code TEXT, name TEXT);
CREATE TEMP TABLE import_prices (
    stock_code TEXT, trading_date DATE, open_price NUMERIC(16,4),
    high_price NUMERIC(16,4), low_price NUMERIC(16,4), close_price NUMERIC(16,4)
);
\copy import_stocks FROM 'data/stocks.csv' WITH (FORMAT csv, HEADER true)
\copy import_prices FROM 'data/prices.csv' WITH (FORMAT csv, HEADER true)
INSERT INTO stocks SELECT * FROM import_stocks ON CONFLICT(code) DO UPDATE SET name=excluded.name;
INSERT INTO prices (stock_code, trading_date, open_price, high_price, low_price, close_price)
SELECT stock_code, trading_date, open_price, high_price, low_price, close_price FROM import_prices
ON CONFLICT(stock_code,trading_date) DO UPDATE SET
    open_price=excluded.open_price, high_price=excluded.high_price,
    low_price=excluded.low_price, close_price=excluded.close_price;
COMMIT;
