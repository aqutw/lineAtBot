package web

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aqutw/lineAtBot/data"
	"github.com/aqutw/lineAtBot/internal/bot"
	"github.com/aqutw/lineAtBot/internal/openrouter"
	"github.com/aqutw/lineAtBot/internal/stock"
	"github.com/aqutw/lineAtBot/internal/testfixture"
)

type fakeModel struct {
	mu        sync.Mutex
	calls     int
	sql, text string
}

func (p *fakeModel) Complete(_ context.Context, messages []openrouter.Message, _ []openrouter.Tool, _ string) (openrouter.Completion, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if messages[len(messages)-1].Role == "tool" || p.sql == "" {
		return openrouter.Completion{Model: "test-model", Message: openrouter.Message{Role: "assistant", Content: p.text}}, nil
	}
	var call openrouter.ToolCall
	call.ID = "test-query"
	call.Type = "function"
	call.Function.Name = "query_stock_database"
	args, _ := json.Marshal(map[string]string{"sql": p.sql})
	call.Function.Arguments = string(args)
	return openrouter.Completion{Model: "test-model", Message: openrouter.Message{Role: "assistant", ToolCalls: []openrouter.ToolCall{call}}}, nil
}

type reply struct{ token, text string }
type fakeLINE struct{ replies chan reply }

func (l *fakeLINE) Reply(_ context.Context, token, text string) error {
	l.replies <- reply{token, text}
	return nil
}
func fixture(t *testing.T) (*Server, *fakeModel, *fakeLINE) {
	return fixtureWithData(t, testfixture.Snapshot)
}
func fixtureWithData(t *testing.T, files fs.FS) (*Server, *fakeModel, *fakeLINE) {
	t.Helper()
	db, err := stock.NewStore(context.Background(), files, "", "")
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeModel{sql: "SELECT s.name AS stock_name, s.code AS stock_id, p.trading_date AS date, p.open_price AS open, p.low_price AS low, p.high_price AS high, p.close_price AS close FROM prices p JOIN stocks s ON s.code=p.stock_code WHERE s.code='2330' ORDER BY p.trading_date DESC LIMIT 1", text: "模型自己的原文：台積電收盤 105.00 元，來自模擬測試資料。"}
	l := &fakeLINE{replies: make(chan reply, 20)}
	ctx, cancel := context.WithCancel(context.Background())
	s := New(ctx, &bot.Service{Store: db, Model: p, Now: func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, stock.Taipei) }}, l,
		Config{Secret: "test-secret", LINEEnabled: true, ModelEnabled: true, DemoToken: "test-demo-token"}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { cancel(); s.Wait(); db.Close() })
	return s, p, l
}
func signature(body []byte) string {
	h := hmac.New(sha256.New, []byte("test-secret"))
	h.Write(body)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
func post(s *Server, path string, body []byte, sig, auth string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	r.Header.Set("X-Line-Signature", sig)
	r.Header.Set("Authorization", auth)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}
func TestSignedWebhookEndToEndAndRedelivery(t *testing.T) {
	s, p, l := fixture(t)
	body := []byte(`{"events":[{"type":"message","webhookEventId":"event-1","replyToken":"reply-1","message":{"type":"text","text":"台積電最近收盤多少？"}}]}`)
	if w := post(s, "/line/webhook", body, "invalid", ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := post(s, "/line/webhook", body, signature(body), ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	select {
	case got := <-l.replies:
		if got.token != "reply-1" || !strings.Contains(got.text, "105.00") || !strings.Contains(got.text, "模擬") || strings.Contains(got.text, "成交量") {
			t.Fatal(got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no reply")
	}
	if w := post(s, "/line/webhook", body, signature(body), ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	p.mu.Lock()
	calls := p.calls
	p.mu.Unlock()
	if calls != 2 {
		t.Fatalf("redelivery called model %d times", calls)
	}
	select {
	case <-l.replies:
		t.Fatal("duplicate reply")
	default:
	}
	// Whitespace changes the signature: validation must use the original raw body.
	changed := append([]byte(" "), body...)
	if w := post(s, "/line/webhook", changed, signature(body), ""); w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestSignedWebhookRepairsLeakedToolMarkupThroughRealAPIClient(t *testing.T) {
	s, _, line := fixture(t)
	var calls atomic.Int32
	final := "台積電（2330）在測試資料中有 6 筆歷史股價。"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages   []openrouter.Message `json:"messages"`
			ToolChoice string               `json:"tool_choice"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		message := openrouter.Message{Role: "assistant"}
		reason := "stop"
		switch calls.Add(1) {
		case 1:
			message.Content = `<dots_function_call><invoke name="query_stock_database"><parameter name="sql">DELETE FROM prices</parameter></invoke></dots_function_call>`
		case 2:
			if !strings.Contains(input.Messages[len(input.Messages)-1].Content, "正式 tool_calls") {
				t.Error("missing repair feedback")
			}
			var call openrouter.ToolCall
			call.ID, call.Type, call.Function.Name = "actual-query", "function", "query_stock_database"
			call.Function.Arguments = `{"sql":"SELECT COUNT(*) AS n FROM price_history WHERE stock_code='2330'"}`
			message.ToolCalls, reason = []openrouter.ToolCall{call}, "tool_calls"
		case 3:
			last := input.Messages[len(input.Messages)-1]
			if input.ToolChoice != "none" || last.Role != "tool" || !strings.Contains(last.Content, `"n":6`) {
				t.Error("missing query evidence", input)
			}
			message.Content = final
		default:
			t.Error("exceeded LLM budget")
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "mock-free-model", "choices": []any{map[string]any{"finish_reason": reason, "message": message}}})
	}))
	defer upstream.Close()
	client := openrouter.New("test-key", "openrouter/free")
	client.URL = upstream.URL
	s.Bot.Model = client
	body := []byte(`{"events":[{"type":"message","webhookEventId":"protocol-test","replyToken":"reply-protocol","message":{"type":"text","text":"2330有幾筆資料？"}}]}`)
	if w := post(s, "/line/webhook", body, signature(body), ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	select {
	case got := <-line.replies:
		if got.text != final || got.token != "reply-protocol" || calls.Load() != 3 {
			t.Fatal(got, calls.Load())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("no repaired reply")
	}
	stats, err := s.Bot.Store.Stats(context.Background())
	if err != nil || stats.Prices != 30 {
		t.Fatal("textual DELETE was executed", stats, err)
	}
}
func TestVerificationAndProtectedAPI(t *testing.T) {
	s, _, _ := fixture(t)
	empty := []byte(`{"events":[]}`)
	if w := post(s, "/line/webhook", empty, signature(empty), ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	body := []byte(`{"text":"台積電最近收盤多少"}`)
	if w := post(s, "/api/query", body, "", ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	w := post(s, "/api/query", body, "", "Bearer test-demo-token")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var answer bot.Answer
	if json.Unmarshal(w.Body.Bytes(), &answer) != nil || len(answer.Queries) != 1 || len(answer.Queries[0].Rows) != 1 {
		t.Fatal(w.Body.String())
	}
	price := answer.Queries[0].Rows[0]
	if price["close"] != "105.00" || price["open"] != "104.50" || price["high"] != "106.00" || price["low"] != "104.00" {
		t.Fatal(w.Body.String())
	}
	if answer.Text != "模型自己的原文：台積電收盤 105.00 元，來自模擬測試資料。" {
		t.Fatal(answer.Text)
	}

	s.Config.DemoToken = ""
	if w := post(s, "/api/query", body, "", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestNoFallbackToOldPriceForToday(t *testing.T) {
	s, p, _ := fixture(t)
	p.sql = "SELECT close_price AS close FROM prices WHERE stock_code='2330' AND trading_date='2026-10-14'"
	p.text = "今天沒有已入庫行情，不以其他日期替代。"
	w := post(s, "/api/query", []byte(`{"text":"台積電今天收盤多少"}`), "", "Bearer test-demo-token")
	if !strings.Contains(w.Body.String(), "沒有已入庫") || strings.Contains(w.Body.String(), "105.00") {
		t.Fatal(w.Body.String())
	}
}

func TestPromptLogsRequireSignatureAndDeduplicate(t *testing.T) {
	s, _, l := fixture(t)
	var logs bytes.Buffer
	s.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	prompt := "你是什麼大語言模型LLM？\n包含\"引號\"的提問"
	body, err := json.Marshal(map[string]any{"events": []any{map[string]any{
		"type": "message", "webhookEventId": "logged-event", "replyToken": "private-reply-token",
		"source":  map[string]string{"userId": "private-user-id"},
		"message": map[string]string{"type": "text", "text": prompt},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if w := post(s, "/line/webhook", body, "invalid", ""); w.Code != 403 || logs.Len() != 0 {
		t.Fatal("unsigned request was logged")
	}
	for i := 0; i < 2; i++ {
		if w := post(s, "/line/webhook", body, signature(body), ""); w.Code != 200 {
			t.Fatal(w.Code)
		}
	}
	select {
	case <-l.replies:
	case <-time.After(2 * time.Second):
		t.Fatal("no reply")
	}
	entries := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(entries) != 1 {
		t.Fatal("expected one log entry for a redelivered event", logs.String())
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(entries[0]), &entry); err != nil {
		t.Fatal(err)
	}
	if entry["msg"] != "user_prompt" || entry["prompt"] != prompt || entry["event_id"] != "logged-event" || entry["channel"] != "line" {
		t.Fatal(entry)
	}
	if strings.Contains(logs.String(), "private-reply-token") || strings.Contains(logs.String(), "private-user-id") {
		t.Fatal("log exposed credentials or LINE user identity")
	}
}

func TestImportedDataRepliesUseActualCSVAndNoDemoLabel(t *testing.T) {
	s, p, l := fixtureWithData(t, data.Snapshot)
	p.text = "台積電（2330）2026-10-14｜收盤 2490 元。資料來源：使用者匯入 CSV（非即時行情）。"
	body := []byte(`{"events":[{"type":"message","webhookEventId":"imported-event","replyToken":"reply-imported","message":{"type":"text","text":"2330最新股價"}}]}`)
	if w := post(s, "/line/webhook", body, signature(body), ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	select {
	case got := <-l.replies:
		if !strings.Contains(got.text, "2026-10-14｜收盤 2490 元") || !strings.Contains(got.text, "使用者匯入 CSV") || strings.Contains(got.text, "DEMO") || strings.Contains(got.text, "模擬") || strings.Contains(got.text, "非真實行情") {
			t.Fatal(got.text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no imported-data reply")
	}
}
