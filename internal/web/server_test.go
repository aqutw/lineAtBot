package web

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aqutw/lineAtBot/data"
	"github.com/aqutw/lineAtBot/internal/bot"
	"github.com/aqutw/lineAtBot/internal/stock"
)

type fakeParser struct {
	mu    sync.Mutex
	calls int
	q     stock.Query
}

func (p *fakeParser) Parse(context.Context, string, time.Time) (stock.Query, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.q, nil
}

type reply struct{ token, text string }
type fakeLINE struct{ replies chan reply }

func (l *fakeLINE) Reply(_ context.Context, token, text string) error {
	l.replies <- reply{token, text}
	return nil
}
func fixture(t *testing.T) (*Server, *fakeParser, *fakeLINE) {
	t.Helper()
	db, err := stock.NewStore(context.Background(), data.Snapshot, "", "")
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeParser{q: stock.Query{Intent: "latest_close", Stock: "2330", Limit: 1}}
	l := &fakeLINE{replies: make(chan reply, 20)}
	ctx, cancel := context.WithCancel(context.Background())
	s := New(ctx, &bot.Service{Store: db, Parser: p, Now: func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, stock.Taipei) }}, l,
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
	if calls != 1 {
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
	if json.Unmarshal(w.Body.Bytes(), &answer) != nil || answer.Result == nil || answer.Result.Prices[0].Close != "105.00" {
		t.Fatal(w.Body.String())
	}
	price := answer.Result.Prices[0]
	if price.Open == nil || *price.Open != "104.50" || price.High == nil || *price.High != "106.00" || price.Low == nil || *price.Low != "104.00" {
		t.Fatal("API omitted OHLC", w.Body.String())
	}
	s.Config.DemoToken = ""
	if w := post(s, "/api/query", body, "", ""); w.Code != 404 {
		t.Fatal(w.Code)
	}
}
func TestNoFallbackToOldPriceForToday(t *testing.T) {
	s, p, _ := fixture(t)
	p.q = stock.Query{Intent: "price_on_date", Stock: "2330", Date: "2026-10-14", Limit: 1}
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
