package stock

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mattn/go-sqlite3"
)

func init() {
	sql.Register("stock_readonly", &sqlite3.SQLiteDriver{ConnectHook: func(c *sqlite3.SQLiteConn) error {
		c.SetLimit(sqlite3.SQLITE_LIMIT_LENGTH, 128*1024)
		c.SetLimit(sqlite3.SQLITE_LIMIT_SQL_LENGTH, 16*1024)
		c.SetLimit(sqlite3.SQLITE_LIMIT_EXPR_DEPTH, 50)
		c.SetLimit(sqlite3.SQLITE_LIMIT_COMPOUND_SELECT, 10)
		c.RegisterAuthorizer(authorizeStockRead)
		return nil
	}})
}

func authorizeStockRead(action int, arg1, arg2, database string) int {
	switch action {
	case sqlite3.SQLITE_SELECT:
		return sqlite3.SQLITE_OK
	case sqlite3.SQLITE_READ:
		// SQLite omits the database/column for optimized COUNT(*) reads.
		if database != "main" && !(database == "" && arg2 == "") {
			return sqlite3.SQLITE_DENY
		}
		switch arg1 {
		case "stocks", "prices", "aliases", "stock_groups", "stock_group_members":
			return sqlite3.SQLITE_OK
		}
	case sqlite3.SQLITE_FUNCTION:
		switch strings.ToLower(arg2) {
		case "count", "max", "min", "avg", "sum", "total", "coalesce", "ifnull", "nullif", "round", "abs",
			"lower", "upper", "length", "substr", "substring", "trim", "ltrim", "rtrim", "replace", "like", "glob", "instr",
			"date", "time", "datetime", "strftime", "julianday", "unixepoch", "printf", "format", "iif",
			"group_concat", "string_agg", "row_number", "rank", "dense_rank", "lag", "lead", "first_value", "last_value", "nth_value", "ntile", "percent_rank", "cume_dist",
			"json_group_array", "json_group_object", "json_array", "json_object", "json_extract":
			return sqlite3.SQLITE_OK
		}
	}
	return sqlite3.SQLITE_DENY
}

type SQLResult struct {
	Rows      []map[string]any `json:"rows"`
	Truncated bool             `json:"truncated"`
	Source    string           `json:"source"`
	Demo      bool             `json:"demo_data"`
}

func (s *Store) Dialect() string {
	if s.postgres {
		return "PostgreSQL"
	}
	return "SQLite"
}

func (s *Store) Catalog(ctx context.Context) ([]Stock, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT code, name FROM stocks ORDER BY code")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stock
	for rows.Next() {
		var item Stock
		if err := rows.Scan(&item.Code, &item.Name); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// Select runs model-generated SQL against the read-only stock database.
// The SQLite connection authorizer additionally prevents file access, PRAGMAs,
// schema changes, unrelated tables and extension functions.
func (s *Store) Select(ctx context.Context, query string) (SQLResult, error) {
	query = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(query), ";"))
	words := strings.Fields(strings.ToUpper(query))
	if len(query) > 12000 || strings.Contains(query, ";") || len(words) == 0 || (words[0] != "SELECT" && words[0] != "WITH") {
		return SQLResult{}, fmt.Errorf("query_rejected: submit one SELECT or WITH statement")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// The wrapper bounds data sent to the model while preserving inner ORDER BY.
	bounded := "SELECT * FROM (\n" + query + "\n) AS stock_result LIMIT 201"
	var rows *sql.Rows
	var err error
	if s.postgres {
		tx, txErr := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if txErr != nil {
			return SQLResult{}, fmt.Errorf("query_failed: database unavailable")
		}
		defer tx.Rollback()
		rows, err = tx.QueryContext(ctx, bounded)
	} else {
		rows, err = s.db.QueryContext(ctx, bounded)
	}
	if err != nil {
		return SQLResult{}, fmt.Errorf("query_failed: check SQL columns, syntax and read-only permissions")
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil || len(columns) > 32 {
		return SQLResult{}, fmt.Errorf("query_failed: too many result columns")
	}
	result := SQLResult{Rows: []map[string]any{}, Source: s.Source, Demo: s.Demo}
	size := 0
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return SQLResult{}, fmt.Errorf("query_failed: cannot read result")
		}
		row := make(map[string]any, len(columns))
		for i, name := range columns {
			if value, ok := values[i].([]byte); ok {
				values[i] = string(value)
			}
			row[name] = values[i]
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			return SQLResult{}, fmt.Errorf("query_failed: cannot encode result")
		}
		if len(result.Rows) == 200 || size+len(encoded) > 64*1024 {
			result.Truncated = true
			break
		}
		size += len(encoded)
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return SQLResult{}, fmt.Errorf("query_failed: database timeout or read error")
	}
	return result, nil
}
