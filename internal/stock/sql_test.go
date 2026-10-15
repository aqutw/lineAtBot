package stock

import (
	"context"
	"testing"
	"time"
)

func TestModelSQLReadsMultipleStocksAndOHLC(t *testing.T) {
	s := demoStore(t)
	result, err := s.Select(context.Background(), `WITH wanted AS (SELECT code, name FROM stocks WHERE code IN ('2330','2454'))
SELECT s.code AS stock_id, s.name AS stock_name, p.trading_date AS date,
p.open_price AS open, p.low_price AS low, p.high_price AS high, p.close_price AS close
FROM prices p JOIN wanted s ON s.code=p.stock_code
WHERE p.trading_date IN ('2026-10-01','2026-10-02') ORDER BY s.code,p.trading_date;`)
	if err != nil || len(result.Rows) != 4 || result.Truncated {
		t.Fatalf("%+v %v", result, err)
	}
	row := result.Rows[0]
	if row["stock_name"] != "台積電" || row["stock_id"] != "2330" || row["open"] != "99.50" || row["close"] != "100.00" {
		t.Fatal(row)
	}
}

func TestModelSQLCannotWriteReadFilesOrInspectOtherTables(t *testing.T) {
	s := demoStore(t)
	for _, query := range []string{
		"DELETE FROM prices", "UPDATE stocks SET name='changed'", "DROP TABLE stocks",
		"ATTACH DATABASE '/tmp/other.sqlite' AS other", "PRAGMA query_only=OFF",
		"SELECT 1; DELETE FROM prices", "SELECT * FROM sqlite_master",
		"SELECT load_extension('anything')", "SELECT readfile('/etc/passwd')",
		"SELECT * FROM pragma_table_info('stocks')",
		"WITH RECURSIVE numbers(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM numbers) SELECT x FROM numbers",
	} {
		t.Run(query, func(t *testing.T) {
			if _, err := s.Select(context.Background(), query); err == nil {
				t.Fatal("unsafe SQL accepted")
			}
		})
	}
	stats, err := s.Stats(context.Background())
	if err != nil || stats.Stocks != 5 || stats.Prices != 30 {
		t.Fatalf("data changed: %+v %v", stats, err)
	}
}

func TestModelSQLTruncatesAndCancels(t *testing.T) {
	s := demoStore(t)
	result, err := s.Select(context.Background(), "SELECT p.close_price FROM prices p CROSS JOIN prices p2")
	if err != nil || !result.Truncated || len(result.Rows) != 200 {
		t.Fatalf("%+v %v", result, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if _, err := s.Select(ctx, "SELECT * FROM prices"); err == nil {
		t.Fatal("cancelled SQL executed")
	}
}
