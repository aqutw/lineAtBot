package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aqutw/lineAtBot/internal/line"
	"github.com/aqutw/lineAtBot/internal/openrouter"
)

func TestSignedWebhookRecovers504AndReports429(t *testing.T) {
	for _, status := range []int{504, 429} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s, _, replies := fixture(t)
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				if call == 1 {
					w.Header().Set("X-Request-ID", "failed-generation")
					w.WriteHeader(status)
					json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": status, "message": "private upstream details must not reach LINE"}})
					return
				}
				message := openrouter.Message{Content: "查到30筆資料。"}
				reason := "stop"
				if call == 2 {
					tool := openrouter.ToolCall{ID: "actual-sql", Type: "function"}
					tool.Function.Name, tool.Function.Arguments = "query_stock_database", `{"sql":"SELECT COUNT(*) AS n FROM price_history"}`
					message = openrouter.Message{ToolCalls: []openrouter.ToolCall{tool}}
					reason = "tool_calls"
				}
				json.NewEncoder(w).Encode(map[string]any{"model": "recovered-free-model", "choices": []any{map[string]any{"finish_reason": reason, "message": message}}})
			}))
			defer upstream.Close()
			client := openrouter.New("test-key", "openrouter/free")
			client.URL = upstream.URL
			s.Bot.Model = client
			body := []byte(`{"events":[{"type":"message","webhookEventId":"recovery","replyToken":"reply-recovery","message":{"type":"text","text":"資料筆數"}}]}`)
			if w := post(s, "/line/webhook", body, signature(body), ""); w.Code != 200 {
				t.Fatal(w.Code)
			}
			select {
			case got := <-replies.replies:
				if strings.Contains(got.text, "private upstream") {
					t.Fatal("upstream error leaked", got)
				}
				if status == 504 && (got.text != "查到30筆資料。" || calls.Load() != 3) {
					t.Fatal(got, calls.Load())
				}
				if status == 429 && (!strings.Contains(got.text, "不一定代表每日免費額度已用完") || calls.Load() != 1) {
					t.Fatal(got, calls.Load())
				}
			case <-time.After(15 * time.Second):
				t.Fatal("no LINE reply")
			}
		})
	}
}

type replyFunc func(context.Context, string, string) error

func (f replyFunc) Reply(ctx context.Context, token, text string) error { return f(ctx, token, text) }

func TestQueuedJobGetsBusyReplyAndReplyHasIndependentDeadline(t *testing.T) {
	s, _, _ := fixture(t)
	var e line.Event
	e.ID, e.ReplyToken, e.Message.Text = "queued-event", "reply", "查價"
	for _, age := range []time.Duration{43 * time.Second, 30 * time.Second} {
		called := false
		s.LINE = replyFunc(func(ctx context.Context, token, text string) error {
			called = true
			deadline, ok := ctx.Deadline()
			if ctx.Err() != nil || !ok || time.Until(deadline) > 8*time.Second {
				t.Error("reply deadline lost", ctx.Err(), deadline)
			}
			if age == 43*time.Second && !strings.Contains(text, "服務忙碌") {
				t.Error(text)
			}
			return nil
		})
		s.process(job{event: e, received: time.Now().Add(-age)})
		if !called {
			t.Fatal("job silently dropped")
		}
	}
	// An expired job must not use an expired reply token.
	s.LINE = replyFunc(func(context.Context, string, string) error { t.Error("expired job sent a reply"); return nil })
	s.process(job{event: e, received: time.Now().Add(-53 * time.Second)})
}

func TestHTTPBodyReadCancellationProducesTimeoutReply(t *testing.T) {
	s, _, _ := fixture(t)
	client := openrouter.New("test-key", "")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer upstream.Close()
	client.URL = upstream.URL
	s.Bot.Model = client
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	answer := s.answer(ctx, "查價")
	if !strings.Contains(answer.Text, "等待逾時") {
		t.Fatal(answer)
	}
}
