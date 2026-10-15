package stock

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/aqutw/lineAtBot/data"
	"github.com/aqutw/lineAtBot/internal/testfixture"
)

func demoStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(context.Background(), testfixture.Snapshot, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

var testNow = time.Date(2026, 10, 14, 12, 0, 0, 0, Taipei)

func TestQueriesAndReadOnlyDatabase(t *testing.T) {
	s := demoStore(t)
	stats, err := s.Stats(context.Background())
	if err != nil || stats.Stocks != 5 || stats.Prices != 30 {
		t.Fatalf("stats: %+v %v", stats, err)
	}
	for _, test := range []struct {
		name  string
		q     Query
		count int
		first string
	}{
		{"latest alias", Query{Intent: "latest_close", Stock: "TSMC"}, 1, "2026-10-13"},
		{"exact date", Query{Intent: "price_on_date", Stock: "2330", Date: "2026-10-01"}, 1, "2026-10-01"},
		{"history", Query{Intent: "recent_history", Stock: "聯發科", Limit: 5}, 5, "2026-10-13"},
		{"range", Query{Intent: "date_range", Stock: "0050", StartDate: "2026-10-01", EndDate: "2026-10-06"}, 4, "2026-10-06"},
		{"today no fallback", Query{Intent: "price_on_date", Stock: "2330", Date: "2026-10-14"}, 0, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			out, err := s.Query(context.Background(), test.q, testNow)
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Prices) != test.count {
				t.Fatalf("prices: %+v", out.Prices)
			}
			if test.count > 0 && out.Prices[0].Date != test.first {
				t.Fatal(out.Prices)
			}
			if !out.Demo {
				t.Fatal("simulated data missing label")
			}
		})
	}
	_, err = s.Lookup(context.Background(), "2330' OR 1=1 --")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("injection not rejected: %v", err)
	}
	if _, err = s.db.Exec("DELETE FROM prices"); err == nil {
		t.Fatal("demo connection allowed writes")
	}
	stats, _ = s.Stats(context.Background())
	if stats.Prices != 30 {
		t.Fatal("read-only test changed data")
	}
}

func TestRejectUnsafeQueryLimitsAndDates(t *testing.T) {
	for _, q := range []Query{
		{Intent: "delete", Stock: "2330"}, {Intent: "latest_close", Stock: ""},
		{Intent: "recent_history", Stock: "2330", Limit: 21}, {Intent: "recent_history", Stock: "2330", Limit: 0},
		{Intent: "price_on_date", Stock: "2330", Date: "2026-02-30"}, {Intent: "price_on_date", Stock: "2330", Date: "2026-10-15"},
		{Intent: "date_range", Stock: "2330", StartDate: "2026-10-10", EndDate: "2026-10-01"},
		{Intent: "date_range", Stock: "2330", StartDate: "2025-01-01", EndDate: "2026-10-01"},
	} {
		if q.Validate(testNow) == nil {
			t.Errorf("accepted invalid query: %+v", q)
		}
	}
}

func TestPostgresPlaceholders(t *testing.T) {
	s := &Store{postgres: true}
	if got := s.bind("WHERE stock_code = ? AND trading_date BETWEEN ? AND ? LIMIT ?"); got != "WHERE stock_code = $1 AND trading_date BETWEEN $2 AND $3 LIMIT $4" {
		t.Fatal(got)
	}
}

func TestDailyOHLCAndCloseOnlyRecords(t *testing.T) {
	stocks, err := testfixture.Snapshot.ReadFile("stocks.csv")
	if err != nil {
		t.Fatal(err)
	}
	prices, err := testfixture.Snapshot.ReadFile("prices.csv")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, values     string
		wantErr, missing bool
	}{
		{"daily OHLC", "104.50,106.00,104.00,105.00", false, false},
		{"missing historical OHLC", ",,,105.00", false, true},
		{"high below close", "104.50,104.75,104.00,105.00", true, false},
		{"low above open", "104.50,106.00,104.75,105.00", true, false},
		{"high below low", "104.50,104.00,106.00,105.00", true, false},
		{"negative price", "-1,106.00,104.00,105.00", true, false},
		{"missing required close", "104.50,106.00,104.00,", true, false},
		{"too much decimal precision", "104.50001,106.00,104.00,105.00", true, false},
		{"too many integer digits", "1000000000000,106.00,104.00,105.00", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			files := fstest.MapFS{
				"dataset.json": &fstest.MapFile{Data: []byte(`{"demo_data":true,"source":"test fixture"}`)},
				"groups.csv":   &fstest.MapFile{Data: []byte("group_name,stock_code\n")},
				"stocks.csv":   &fstest.MapFile{Data: stocks},
				"prices.csv":   &fstest.MapFile{Data: []byte(strings.Replace(string(prices), "2330,2026-10-13,104.50,106.00,104.00,105.00", "2330,2026-10-13,"+test.values, 1))},
			}
			s, err := NewStore(context.Background(), files, "", "")
			if test.wantErr {
				if err == nil {
					s.Close()
					t.Fatal("invalid CSV was accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			out, err := s.Query(context.Background(), Query{Intent: "latest_close", Stock: "2330"}, testNow)
			if err != nil || len(out.Prices) != 1 {
				t.Fatalf("query: %+v %v", out, err)
			}
			p := out.Prices[0]
			if p.Close != "105.00" {
				t.Fatal(p)
			}
			if test.missing {
				if p.Open != nil || p.High != nil || p.Low != nil {
					t.Fatal("missing OHLC must not be fabricated", p)
				}
			} else if p.Open == nil || *p.Open != "104.50" || p.High == nil || *p.High != "106.00" || p.Low == nil || *p.Low != "104.00" {
				t.Fatal("OHLC did not round-trip", p)
			}
		})
	}
}

func TestImportedDatasetReplacesSimulatedData(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s, err := NewStore(ctx, data.Snapshot, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stats, err := s.Stats(ctx)
	if err != nil || stats.Stocks != 200 || stats.Prices != 193541 || stats.LatestDate != "2026-10-14" {
		t.Fatalf("imported stats: %+v %v", stats, err)
	}
	if s.Demo || s.Source != "使用者匯入 CSV（非即時行情）" {
		t.Fatalf("imported data was marked as simulated: %+v", s)
	}
	var groups, members int
	if err := s.db.QueryRowContext(ctx, "SELECT (SELECT COUNT(*) FROM stock_groups), COUNT(*) FROM stock_group_members").Scan(&groups, &members); err != nil || groups != 2 || members != 200 {
		t.Fatalf("groups=%d members=%d err=%v", groups, members, err)
	}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, Taipei)
	for _, test := range []struct{ code, name, open, high, low, close string }{
		{"1471", "首利", "9.3", "9.3", "9.17", "9.27"},
		{"2059", "川湖", "10155", "10295", "9950", "10005"},
	} {
		out, err := s.Query(ctx, Query{Intent: "latest_close", Stock: test.code}, now)
		if err != nil || len(out.Prices) != 1 || out.Stock.Name != test.name || out.Demo {
			t.Fatalf("imported query: %+v %v", out, err)
		}
		p := out.Prices[0]
		if p.Date != "2026-10-14" || p.Close != test.close || p.Open == nil || *p.Open != test.open || p.High == nil || *p.High != test.high || p.Low == nil || *p.Low != test.low {
			t.Fatalf("imported OHLC differs from CSV: %+v", p)
		}
	}
	if _, err := s.Lookup(ctx, "0050"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old demo-only stock survived replacement: %v", err)
	}
}
