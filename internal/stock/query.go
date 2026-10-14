package stock

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var Taipei = time.FixedZone("Asia/Taipei", 8*60*60)

// Query is a constrained intent, never executable model-generated SQL.
type Query struct {
	Intent    string `json:"intent"`
	Stock     string `json:"stock"`
	Date      string `json:"date"`
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	Limit     int    `json:"limit"`
}

func (q Query) Validate(now time.Time) error {
	switch q.Intent {
	case "help", "clarify", "unsupported":
		return nil
	case "latest_close", "price_on_date", "recent_history", "date_range":
	default:
		return errors.New("unsupported intent")
	}
	if strings.TrimSpace(q.Stock) == "" || len([]rune(q.Stock)) > 40 {
		return errors.New("invalid stock reference")
	}
	parse := func(s string) (time.Time, error) {
		t, err := time.ParseInLocation("2006-01-02", s, Taipei)
		if err != nil || t.Year() < 1990 || t.After(now.In(Taipei)) {
			return time.Time{}, fmt.Errorf("invalid or future date: %q", s)
		}
		return t, nil
	}
	if q.Intent == "price_on_date" {
		if _, err := parse(q.Date); err != nil {
			return err
		}
	}
	if q.Intent == "recent_history" && (q.Limit < 1 || q.Limit > 20) {
		return errors.New("history limit must be 1..20")
	}
	if q.Intent == "date_range" {
		start, err := parse(q.StartDate)
		if err != nil {
			return err
		}
		end, err := parse(q.EndDate)
		if err != nil {
			return err
		}
		if end.Before(start) || end.Sub(start) > 90*24*time.Hour {
			return errors.New("date range must be within 90 days")
		}
	}
	return nil
}
