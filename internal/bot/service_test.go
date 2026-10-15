package bot

import (
	"strings"
	"testing"

	"github.com/aqutw/lineAtBot/internal/stock"
)

func TestImportedPriceReplyHasNoDemoLabel(t *testing.T) {
	text := Format(stock.Query{Intent: "latest_close"}, stock.Result{
		Stock:  stock.Stock{Code: "1471", Name: "首利"},
		Prices: []stock.Price{{Date: "2026-10-14", Close: "9.27"}},
		Source: "使用者匯入 CSV（非即時行情）", Demo: false,
	})
	if strings.Contains(text, "DEMO") || strings.Contains(text, "模擬") || strings.Contains(text, "非真實行情") {
		t.Fatal(text)
	}
	if !strings.Contains(text, "9.27") || !strings.Contains(text, "2026-10-14") || !strings.Contains(text, "使用者匯入 CSV") {
		t.Fatal(text)
	}
}
