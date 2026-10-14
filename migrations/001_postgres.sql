-- Run as the database owner, not as the bot's read-only user.
CREATE TABLE IF NOT EXISTS stocks (
    code TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);
CREATE TABLE IF NOT EXISTS aliases (
    alias TEXT PRIMARY KEY,
    stock_code TEXT NOT NULL REFERENCES stocks(code)
);
CREATE TABLE IF NOT EXISTS prices (
    stock_code TEXT NOT NULL REFERENCES stocks(code),
    trading_date DATE NOT NULL,
    close_price NUMERIC(16, 4) NOT NULL CHECK (close_price >= 0),
    PRIMARY KEY (stock_code, trading_date)
);
-- Grant SELECT on these three tables to your dedicated bot user.
-- The bot additionally connects with default_transaction_read_only=on.
