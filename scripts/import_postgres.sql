-- Usage: see README.md; apply migrations 001, 002 and 003 before importing.
-- Use the owner/importer connection, not the read-only bot connection.
-- Bundled CSVs contain user-provided historical prices; source: data/dataset.json.
BEGIN;
CREATE TEMP TABLE import_stocks (code TEXT, name TEXT);
CREATE TEMP TABLE import_prices (
    stock_code TEXT, trading_date DATE, open_price NUMERIC(16,4),
    high_price NUMERIC(16,4), low_price NUMERIC(16,4), close_price NUMERIC(16,4)
);
CREATE TEMP TABLE import_groups (group_name TEXT, stock_code TEXT);
\copy import_stocks FROM 'data/stocks.csv' WITH (FORMAT csv, HEADER true)
\copy import_prices FROM 'data/prices.csv' WITH (FORMAT csv, HEADER true)
\copy import_groups FROM 'data/groups.csv' WITH (FORMAT csv, HEADER true)
INSERT INTO stocks SELECT * FROM import_stocks ON CONFLICT(code) DO UPDATE SET name=excluded.name;
INSERT INTO prices (stock_code, trading_date, open_price, high_price, low_price, close_price)
SELECT stock_code, trading_date, open_price, high_price, low_price, close_price FROM import_prices
ON CONFLICT(stock_code,trading_date) DO UPDATE SET
    open_price=excluded.open_price, high_price=excluded.high_price,
    low_price=excluded.low_price, close_price=excluded.close_price;
INSERT INTO stock_groups (name) SELECT DISTINCT group_name FROM import_groups ON CONFLICT DO NOTHING;
INSERT INTO stock_group_members (group_name, stock_code) SELECT group_name, stock_code FROM import_groups ON CONFLICT DO NOTHING;
COMMIT;
