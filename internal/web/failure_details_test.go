package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aqutw/lineAtBot/internal/openrouter"
)

func TestSignedWebhookRelaysGoBuiltFailureDetails(t *testing.T) {
	s, _, replies := fixture(t)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"code":429,"message":"private prompt and raw credential","metadata":{"provider_name":"Poolside"}},"openrouter_metadata":{"attempts":[{"provider":"NVIDIA","model":"vendor/model-a:free","status":504},{"provider":"Google AI Studio","model":"google/model-b:free","status":429},{"provider":"Poolside","status":429}],"endpoints":{"available":[{"provider":"untried-provider","model":"untried-model"}]}}}`)
	}))
	defer upstream.Close()
	client := openrouter.New("test-key", "openrouter/free")
	client.URL = upstream.URL
	s.Bot.Model = client
	body := []byte(`{"events":[{"type":"message","webhookEventId":"failure-details","replyToken":"reply-details","source":{"type":"user","userId":"U-test-user"},"message":{"type":"text","text":"2330 最新股價"}}]}`)
	if w := post(s, "/line/webhook", body, signature(body), ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	select {
	case got := <-replies.replies:
		for _, want := range []string{"vendor/model-a:free／NVIDIA：回應逾時（504）", "google/model-b:free／Google AI Studio：請求受限", "模型型號未提供／Poolside", "可能不含完整上游嘗試紀錄"} {
			if !strings.Contains(got.text, want) {
				t.Fatal(got.text)
			}
		}
		for _, forbidden := range []string{"openrouter", "private", "credential", "untried"} {
			if strings.Contains(strings.ToLower(got.text), forbidden) {
				t.Fatal(got.text)
			}
		}
		if got.token != "reply-details" || calls.Load() != 1 {
			t.Fatal(got, calls.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no diagnostic reply")
	}
	stats, err := s.Bot.Store.Stats(context.Background())
	if err != nil || stats.Prices != 30 {
		t.Fatal("failed model request changed data", stats, err)
	}
}
