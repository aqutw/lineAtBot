-- Optional upgrade for existing PostgreSQL installations importing groups.csv.
BEGIN;
CREATE TABLE IF NOT EXISTS stock_groups (
    name TEXT PRIMARY KEY
);
CREATE TABLE IF NOT EXISTS stock_group_members (
    group_name TEXT NOT NULL REFERENCES stock_groups(name),
    stock_code TEXT NOT NULL REFERENCES stocks(code),
    PRIMARY KEY (group_name, stock_code)
);
COMMIT;
