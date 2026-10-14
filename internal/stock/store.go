package stock

import (
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db       *sql.DB
	path     string
	postgres bool
	Source   string
	Demo     bool
}
type Stock struct {
	Code string `json:"code"`
	Name string `json:"name"`
}
type Price struct {
	Date  string  `json:"date"`
	Open  *string `json:"open"`
	High  *string `json:"high"`
	Low   *string `json:"low"`
	Close string  `json:"close"`
}
type Result struct {
	Stock      Stock   `json:"stock"`
	Prices     []Price `json:"prices"`
	LatestDate string  `json:"latest_available_date"`
	Source     string  `json:"source"`
	Demo       bool    `json:"demo_data"`
}
type Stats struct {
	Stocks     int    `json:"stocks"`
	Prices     int    `json:"prices"`
	LatestDate string `json:"latest_available_date"`
}

func NewStore(ctx context.Context, files fs.FS, databaseURL, source string) (*Store, error) {
	if databaseURL != "" {
		u, err := url.Parse(databaseURL)
		if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
			return nil, fmt.Errorf("DATABASE_URL must be a PostgreSQL connection URL")
		}
		params := u.Query()
		if params.Get("sslmode") == "" {
			params.Set("sslmode", "require")
		}
		params.Set("default_transaction_read_only", "on")
		params.Set("statement_timeout", "3000")
		u.RawQuery = params.Encode()
		db, err := sql.Open("postgres", u.String())
		if err != nil {
			return nil, fmt.Errorf("database configuration invalid")
		}
		db.SetMaxOpenConns(4)
		db.SetMaxIdleConns(2)
		db.SetConnMaxLifetime(5 * time.Minute)
		if err = db.PingContext(ctx); err != nil {
			db.Close()
			return nil, fmt.Errorf("database connection failed")
		}
		if source == "" {
			source = "使用者匯入行情（非即時報價）"
		}
		s := &Store{db: db, postgres: true, Source: source}
		rows, err := db.QueryContext(ctx, "SELECT open_price, high_price, low_price, close_price FROM prices LIMIT 0")
		if err != nil {
			db.Close()
			return nil, fmt.Errorf("database OHLC schema missing; run migrations/001_postgres.sql and migrations/002_add_ohlc.sql first")
		}
		rows.Close()
		if _, err = s.Stats(ctx); err != nil {
			db.Close()
			return nil, fmt.Errorf("database schema missing; import migrations/001_postgres.sql first")
		}
		return s, nil
	}
	f, err := os.CreateTemp("", "lineatbot-*.sqlite")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	if err = f.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	writeDB, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?_foreign_keys=on")
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	if err = seed(writeDB, files); err != nil {
		writeDB.Close()
		os.Remove(path)
		return nil, err
	}
	if err = writeDB.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	db, err := sql.Open("sqlite3", "file:"+filepath.ToSlash(path)+"?mode=ro&immutable=1&_query_only=true")
	if err != nil {
		os.Remove(path)
		return nil, err
	}
	db.SetMaxOpenConns(4)
	if err = db.PingContext(ctx); err != nil {
		db.Close()
		os.Remove(path)
		return nil, err
	}
	if source == "" {
		source = "DEMO 模擬資料（非真實行情）"
	}
	return &Store{db: db, path: path, Source: source, Demo: true}, nil
}

func (s *Store) Close() error {
	err := s.db.Close()
	if s.path != "" {
		os.Remove(s.path)
	}
	return err
}

func seed(db *sql.DB, files fs.FS) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`
CREATE TABLE stocks (code TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE);
CREATE TABLE aliases (alias TEXT PRIMARY KEY, stock_code TEXT NOT NULL REFERENCES stocks(code));
CREATE TABLE prices (
 stock_code TEXT NOT NULL REFERENCES stocks(code), trading_date TEXT NOT NULL,
 open_price TEXT CHECK (CAST(open_price AS REAL) >= 0),
 high_price TEXT CHECK (CAST(high_price AS REAL) >= 0),
 low_price TEXT CHECK (CAST(low_price AS REAL) >= 0),
 close_price TEXT NOT NULL CHECK (CAST(close_price AS REAL) >= 0),
 CHECK (
  (high_price IS NULL OR low_price IS NULL OR CAST(high_price AS REAL) >= CAST(low_price AS REAL)) AND
  (open_price IS NULL OR high_price IS NULL OR CAST(open_price AS REAL) <= CAST(high_price AS REAL)) AND
  (open_price IS NULL OR low_price IS NULL OR CAST(open_price AS REAL) >= CAST(low_price AS REAL)) AND
  (high_price IS NULL OR CAST(close_price AS REAL) <= CAST(high_price AS REAL)) AND
  (low_price IS NULL OR CAST(close_price AS REAL) >= CAST(low_price AS REAL))
 ),
 PRIMARY KEY (stock_code, trading_date)
);`)
	if err != nil {
		return err
	}
	load := func(path string, header []string) ([][]string, error) {
		f, err := files.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r := csv.NewReader(f)
		r.FieldsPerRecord = len(header)
		rows, err := r.ReadAll()
		if err != nil {
			return nil, err
		}
		if len(rows) < 2 || strings.Join(rows[0], ",") != strings.Join(header, ",") {
			return nil, fmt.Errorf("invalid CSV header: %s", path)
		}
		return rows[1:], nil
	}
	stocks, err := load("stocks.csv", []string{"code", "name"})
	if err != nil {
		return err
	}
	for _, row := range stocks {
		if strings.TrimSpace(row[0]) == "" || strings.TrimSpace(row[1]) == "" {
			return fmt.Errorf("empty stock field")
		}
		if _, err = tx.Exec("INSERT INTO stocks VALUES (?, ?)", row[0], row[1]); err != nil {
			return err
		}
	}
	prices, err := load("prices.csv", []string{"stock_code", "trading_date", "open_price", "high_price", "low_price", "close_price"})
	if err != nil {
		return err
	}
	for _, row := range prices {
		if _, err = time.Parse("2006-01-02", row[1]); err != nil {
			return fmt.Errorf("invalid CSV date")
		}
		if err = validateOHLC(row[2:]); err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO prices (stock_code, trading_date, open_price, high_price, low_price, close_price) VALUES (?, ?, ?, ?, ?, ?)", row[0], row[1], nullablePrice(row[2]), nullablePrice(row[3]), nullablePrice(row[4]), row[5]); err != nil {
			return err
		}
	}
	aliases := map[string]string{"台積": "2330", "tsmc": "2330", "富士康": "2317", "台灣50": "0050", "臺灣50": "0050", "元大50": "0050"}
	for alias, code := range aliases {
		_, err = tx.Exec("INSERT INTO aliases SELECT ?, code FROM stocks WHERE code = ?", alias, code)
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Match PostgreSQL NUMERIC(16,4) and compare prices without floating-point rounding.
func priceUnits(s string) (int64, error) {
	if s == "" {
		return 0, fmt.Errorf("missing price")
	}
	for _, c := range s {
		if (c < '0' || c > '9') && c != '.' {
			return 0, fmt.Errorf("invalid price")
		}
	}
	parts := strings.Split(s, ".")
	if len(parts) > 2 || parts[0] == "" || len(strings.TrimLeft(parts[0], "0")) > 12 {
		return 0, fmt.Errorf("invalid price: expected NUMERIC(16,4)")
	}
	fraction := ""
	if len(parts) == 2 {
		fraction = parts[1]
		if fraction == "" || len(fraction) > 4 {
			return 0, fmt.Errorf("invalid price: expected NUMERIC(16,4)")
		}
	}
	units, err := strconv.ParseInt(strings.TrimLeft(parts[0], "0")+fraction+strings.Repeat("0", 4-len(fraction)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid price")
	}
	return units, nil
}

func nullablePrice(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Values are open, high, low, close. Only close is required for legacy records.
func validateOHLC(values []string) error {
	var units [4]int64
	for i, value := range values {
		if value == "" && i < 3 {
			continue
		}
		var err error
		if units[i], err = priceUnits(value); err != nil {
			return err
		}
	}
	for _, pair := range [][2]int{{1, 2}, {1, 0}, {0, 2}, {1, 3}, {3, 2}} {
		if values[pair[0]] != "" && values[pair[1]] != "" && units[pair[0]] < units[pair[1]] {
			return fmt.Errorf("invalid OHLC price range")
		}
	}
	return nil
}

func (s *Store) bind(query string) string {
	if !s.postgres {
		return query
	}
	var out strings.Builder
	n := 0
	for _, c := range query {
		if c == '?' {
			n++
			fmt.Fprintf(&out, "$%d", n)
		} else {
			out.WriteRune(c)
		}
	}
	return out.String()
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var out Stats
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM stocks), COUNT(*), COALESCE(CAST(MAX(trading_date) AS TEXT), '') FROM prices`).Scan(&out.Stocks, &out.Prices, &out.LatestDate)
	return out, err
}

func (s *Store) Lookup(ctx context.Context, reference string) (Stock, error) {
	ref := strings.ToLower(strings.TrimSpace(reference))
	var out Stock
	err := s.db.QueryRowContext(ctx, s.bind(`SELECT code, name FROM stocks
 WHERE lower(code) = ? OR lower(name) = ? OR code IN
 (SELECT stock_code FROM aliases WHERE alias = ?) LIMIT 1`), ref, ref, ref).Scan(&out.Code, &out.Name)
	return out, err
}

func (s *Store) Query(ctx context.Context, q Query, now time.Time) (Result, error) {
	if err := q.Validate(now); err != nil {
		return Result{}, err
	}
	if q.Intent == "help" || q.Intent == "clarify" || q.Intent == "unsupported" {
		return Result{}, fmt.Errorf("intent does not query prices")
	}
	stock, err := s.Lookup(ctx, q.Stock)
	if err != nil {
		return Result{}, err
	}
	out := Result{Stock: stock, Prices: []Price{}, Source: s.Source, Demo: s.Demo}
	err = s.db.QueryRowContext(ctx, s.bind("SELECT COALESCE(CAST(MAX(trading_date) AS TEXT), '') FROM prices WHERE stock_code = ?"), stock.Code).Scan(&out.LatestDate)
	if err != nil {
		return Result{}, err
	}
	query := "SELECT CAST(trading_date AS TEXT), CAST(open_price AS TEXT), CAST(high_price AS TEXT), CAST(low_price AS TEXT), CAST(close_price AS TEXT) FROM prices WHERE stock_code = ?"
	args := []any{stock.Code}
	switch q.Intent {
	case "latest_close":
		query += " ORDER BY trading_date DESC LIMIT 1"
	case "price_on_date":
		query += " AND trading_date = ? ORDER BY trading_date DESC LIMIT 1"
		args = append(args, q.Date)
	case "recent_history":
		query += " ORDER BY trading_date DESC LIMIT ?"
		args = append(args, q.Limit)
	case "date_range":
		query += " AND trading_date BETWEEN ? AND ? ORDER BY trading_date DESC LIMIT 60"
		args = append(args, q.StartDate, q.EndDate)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return Result{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var p Price
		if err = rows.Scan(&p.Date, &p.Open, &p.High, &p.Low, &p.Close); err != nil {
			return Result{}, err
		}
		out.Prices = append(out.Prices, p)
	}
	return out, rows.Err()
}
