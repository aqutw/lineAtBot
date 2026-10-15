-- Run as the database owner, not as the bot's read-only user.
CREATE TABLE IF NOT EXISTS stocks (
    code TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS aliases (
    alias TEXT PRIMARY KEY,
    stock_code TEXT NOT NULL REFERENCES stocks(code)
);
CREATE TABLE IF NOT EXISTS stock_groups (
    name TEXT PRIMARY KEY
);
CREATE TABLE IF NOT EXISTS stock_group_members (
    group_name TEXT NOT NULL REFERENCES stock_groups(name),
    stock_code TEXT NOT NULL REFERENCES stocks(code),
    PRIMARY KEY (group_name, stock_code)
);
CREATE TABLE IF NOT EXISTS prices (
    stock_code TEXT NOT NULL REFERENCES stocks(code),
    trading_date DATE NOT NULL,
    open_price NUMERIC(16, 4) CHECK (open_price >= 0),
    high_price NUMERIC(16, 4) CHECK (high_price >= 0),
    low_price NUMERIC(16, 4) CHECK (low_price >= 0),
    close_price NUMERIC(16, 4) NOT NULL CHECK (close_price >= 0),
    CONSTRAINT prices_ohlc_order CHECK (
        high_price >= low_price AND
        open_price <= high_price AND open_price >= low_price AND
        close_price <= high_price AND close_price >= low_price
    ),
    PRIMARY KEY (stock_code, trading_date)
);
-- Grant SELECT on the stock data tables to your dedicated bot user.
-- The bot additionally connects with default_transaction_read_only=on.
