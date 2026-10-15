package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompleteUsesToolsAndPreservesFreeFormText(t *testing.T) {
	expected := "我透過 OpenRouter 提供服務。\n這是自由格式，包含 open / low / high / close。"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("bad auth")
		}
		var input map[string]any
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			t.Error("bad request")
		}
		if input["model"] != "openrouter/free" || input["tool_choice"] != "auto" || len(input["tools"].([]any)) != 1 {
			t.Error("missing model/tools")
		}
		if _, exists := input["response_format"]; exists {
			t.Error("forced answer schema still present")
		}
		json.NewEncoder(w).Encode(map[string]any{"model": "routed-free-model", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": expected}}}})
	}))
	defer upstream.Close()
	c := New("Bearer test-key", "")
	c.URL = upstream.URL
	got, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "你是什麼LLM？"}}, []Tool{{Type: "function"}}, "auto")
	if err != nil || got.Message.Content != expected || got.Model != "routed-free-model" {
		t.Fatalf("%+v %v", got, err)
	}
}
func TestCompleteAcceptsToolCallAndHandlesQuota(t *testing.T) {
	for _, status := range []int{200, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": "tool_calls", "message": map[string]any{"tool_calls": []any{map[string]any{"id": "call-1", "type": "function", "function": map[string]string{"name": "query_stock_database", "arguments": `{"sql":"SELECT 1"}`}}}}}}})
			}))
			defer server.Close()
			c := New("test-key", "")
			c.URL = server.URL
			got, err := c.Complete(context.Background(), []Message{{Role: "user", Content: "查兩檔OHLC"}}, nil, "auto")
			if status == 200 && (err != nil || len(got.Message.ToolCalls) != 1) {
				t.Fatalf("%+v %v", got, err)
			}
			if status == 429 && !errors.Is(err, ErrRateLimited) {
				t.Fatal(err)
			}
			if status == 500 && !errors.Is(err, ErrUnavailable) {
				t.Fatal(err)
			}
		})
	}
}
func TestCancelledRateWaitDoesNotSendRequest(t *testing.T) {
	c := New("test-key", "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Complete(ctx, nil, nil, "auto"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
