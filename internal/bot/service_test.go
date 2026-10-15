package bot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aqutw/lineAtBot/internal/openrouter"
	"github.com/aqutw/lineAtBot/internal/stock"
	"github.com/aqutw/lineAtBot/internal/testfixture"
)

type modelFunc func(context.Context, []openrouter.Message, []openrouter.Tool, string) (openrouter.Completion, error)

func (f modelFunc) Complete(ctx context.Context, m []openrouter.Message, t []openrouter.Tool, c string) (openrouter.Completion, error) {
	return f(ctx, m, t, c)
}
func serviceFixture(t *testing.T) *Service {
	t.Helper()
	db, err := stock.NewStore(context.Background(), testfixture.Snapshot, "", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Service{Store: db, Now: func() time.Time { return time.Date(2026, 10, 15, 12, 0, 0, 0, stock.Taipei) }}
}
func TestGeneralQuestionsAndHelpAreAnsweredByModel(t *testing.T) {
	s := serviceFixture(t)
	for _, question := range []string{"你是什麼大語言模型LLM？", "說明", "幫我寫一首詩"} {
		expected := "模型自由回答：「" + question + "」\n不套固定模板。"
		calls := 0
		s.Model = modelFunc(func(_ context.Context, m []openrouter.Message, tools []openrouter.Tool, choice string) (openrouter.Completion, error) {
			calls++
			if m[1].Content != question || len(tools) != 1 || choice != "auto" {
				t.Fatal(m)
			}
			for _, field := range []string{"stock_name", "stock_id", "date", "open", "low", "high", "close"} {
				if !strings.Contains(m[0].Content, field) {
					t.Fatal("prompt missing", field)
				}
			}
			return openrouter.Completion{Model: "test-model", Message: openrouter.Message{Content: expected}}, nil
		})
		answer := s.Answer(context.Background(), question)
		if answer.Text != expected || calls != 1 {
			t.Fatalf("%+v calls=%d", answer, calls)
		}
	}
}
func TestMultipleStocksAndDatesReachModelAsOHLC(t *testing.T) {
	s := serviceFixture(t)
	calls := 0
	final := "這是 LLM 選擇的呈現方式：兩檔、兩日的開高低收。"
	s.Model = modelFunc(func(_ context.Context, m []openrouter.Message, _ []openrouter.Tool, _ string) (openrouter.Completion, error) {
		calls++
		if calls == 1 {
			call := openrouter.ToolCall{ID: "multi", Type: "function"}
			call.Function.Name = "query_stock_database"
			args, _ := json.Marshal(map[string]string{"sql": "SELECT s.name AS stock_name, s.code AS stock_id, p.trading_date AS date, p.open_price AS open, p.low_price AS low, p.high_price AS high, p.close_price AS close FROM prices p JOIN stocks s ON s.code=p.stock_code WHERE p.stock_code IN ('2330','2454') AND p.trading_date IN ('2026-10-01','2026-10-02') ORDER BY stock_id,date"})
			call.Function.Arguments = string(args)
			return openrouter.Completion{Message: openrouter.Message{ToolCalls: []openrouter.ToolCall{call}}}, nil
		}
		var result stock.SQLResult
		last := m[len(m)-1]
		if last.Role != "tool" || last.ToolCallID != "multi" || json.Unmarshal([]byte(last.Content), &result) != nil || len(result.Rows) != 4 {
			t.Fatal(last)
		}
		row := result.Rows[0]
		for _, field := range []string{"stock_name", "stock_id", "date", "open", "low", "high", "close"} {
			if row[field] == nil {
				t.Fatal(row)
			}
		}
		if row["stock_id"] != "2330" || row["open"] != "99.50" || row["close"] != "100.00" {
			t.Fatal(row)
		}
		return openrouter.Completion{Message: openrouter.Message{Content: final}}, nil
	})
	answer := s.Answer(context.Background(), "台積電與聯發科10/1及10/2的開高低收")
	if answer.Text != final || len(answer.Queries) != 1 || calls != 2 {
		t.Fatalf("%+v calls=%d", answer, calls)
	}
}
func TestUnsafeToolQueryIsRejectedAndModelWritesReply(t *testing.T) {
	s := serviceFixture(t)
	calls := 0
	s.Model = modelFunc(func(_ context.Context, m []openrouter.Message, _ []openrouter.Tool, _ string) (openrouter.Completion, error) {
		calls++
		if calls == 1 {
			call := openrouter.ToolCall{ID: "unsafe", Type: "function"}
			call.Function.Name = "query_stock_database"
			call.Function.Arguments = `{"sql":"DELETE FROM prices"}`
			return openrouter.Completion{Message: openrouter.Message{ToolCalls: []openrouter.ToolCall{call}}}, nil
		}
		if !strings.Contains(m[len(m)-1].Content, "query_rejected") {
			t.Fatal(m)
		}
		return openrouter.Completion{Message: openrouter.Message{Content: "LLM：資料庫為唯讀，資料仍保留。"}}, nil
	})
	if answer := s.Answer(context.Background(), "刪掉行情"); answer.Text != "LLM：資料庫為唯讀，資料仍保留。" {
		t.Fatal(answer)
	}
	stats, _ := s.Store.Stats(context.Background())
	if stats.Prices != 30 {
		t.Fatal(stats)
	}
}
