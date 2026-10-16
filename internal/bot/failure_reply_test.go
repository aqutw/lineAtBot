package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/aqutw/lineAtBot/internal/openrouter"
)

func TestFailureReplyListsKnownModelsAndReasonsWithoutPlatformOrRawErrors(t *testing.T) {
	err := &openrouter.APIError{Kind: openrouter.RateLimited, Code: 429, Model: "openrouter/free", Attempts: []openrouter.Attempt{
		{Model: "vendor/model-a:free", Provider: "NVIDIA", Kind: openrouter.Timeout, Code: 504},
		{Model: "google/model-b:free", Provider: "Google AI Studio", Kind: openrouter.RateLimited, Code: 429},
	}}
	text := modelFailureReply(err)
	for _, want := range []string{"vendor/model-a:free／NVIDIA：回應逾時（504）", "google/model-b:free／Google AI Studio：請求受限", "不一定代表每日免費額度已用完", "可能不含完整上游嘗試紀錄"} {
		if !strings.Contains(text, want) {
			t.Fatal(text)
		}
	}
	if strings.Contains(strings.ToLower(text), "openrouter") {
		t.Fatal(text)
	}
}

func TestFailureReplyUnknownAndClientTimeoutDoNotBlameLastSuccessfulModel(t *testing.T) {
	text := modelFailureReply(&openrouter.APIError{Kind: openrouter.Timeout, TimeoutSource: "client_deadline", Cause: context.DeadlineExceeded})
	if !strings.Contains(text, "模型型號未提供") || !strings.Contains(text, "尚未取得上游完整明細") || strings.Contains(text, "504") {
		t.Fatal(text)
	}
	text = modelFailureReply(&openrouter.APIError{Kind: openrouter.Unavailable, Model: "sk-or-v1-secret", Provider: "OPENROUTER", Cause: context.DeadlineExceeded})
	if strings.Contains(strings.ToLower(text), "openrouter") || strings.Contains(text, "secret") {
		t.Fatal(text)
	}
}

func TestFailedGoRetryHistoryAppearsOnlyWhenFinalAnswerFails(t *testing.T) {
	for _, recover := range []bool{false, true} {
		s := serviceFixture(t)
		calls := 0
		s.Model = modelFunc(func(context.Context, []openrouter.Message, []openrouter.Tool, string) (openrouter.Completion, error) {
			calls++
			if calls == 1 {
				return openrouter.Completion{}, &openrouter.APIError{Kind: openrouter.Timeout, HTTPStatus: 504, Code: 504, Model: "vendor/model-a:free", Provider: "NVIDIA"}
			}
			if recover {
				return openrouter.Completion{Message: openrouter.Message{Content: "模型正常回答。"}}, nil
			}
			return openrouter.Completion{}, &openrouter.APIError{Kind: openrouter.RateLimited, HTTPStatus: 429, Code: 429, Model: "google/model-b:free", Provider: "Google AI Studio"}
		})
		text := s.Answer(context.Background(), "測試").Text
		if calls != 2 {
			t.Fatal(calls)
		}
		if recover {
			if text != "模型正常回答。" {
				t.Fatal(text)
			}
		} else if !strings.Contains(text, "NVIDIA") || !strings.Contains(text, "Google AI Studio") || strings.Contains(strings.ToLower(text), "openrouter") {
			t.Fatal(text)
		}
	}
}

func TestFailureReplyIsBoundedAndDeduplicated(t *testing.T) {
	err := &openrouter.APIError{Kind: openrouter.Unavailable}
	for i := 0; i < 30; i++ {
		err.Attempts = append(err.Attempts, openrouter.Attempt{Model: "model-" + string(rune('a'+i)), Kind: openrouter.Unavailable, Code: 503})
	}
	text := modelFailureReply(err, err)
	if strings.Count(text, "• ") != 6 || !strings.Contains(text, "前 6 項") {
		t.Fatal(text)
	}
}

func TestLaterTimeoutRetainsReportedFallbackFailureWithoutBlamingSuccessfulModel(t *testing.T) {
	s := serviceFixture(t)
	calls := 0
	s.Model = modelFunc(func(context.Context, []openrouter.Message, []openrouter.Tool, string) (openrouter.Completion, error) {
		calls++
		if calls == 1 {
			return openrouter.Completion{Model: "previously-successful-model", Message: openrouter.Message{ToolCalls: []openrouter.ToolCall{queryCall("q", "SELECT COUNT(*) FROM price_history")}}, FailedAttempts: []openrouter.Attempt{{Model: "failed-fallback-model", Provider: "NVIDIA", Kind: openrouter.Timeout, Code: 504}}}, nil
		}
		return openrouter.Completion{}, &openrouter.APIError{Kind: openrouter.Timeout, TimeoutSource: "client_deadline", Cause: context.DeadlineExceeded}
	})
	text := s.Answer(context.Background(), "查價").Text
	if calls != 2 || !strings.Contains(text, "failed-fallback-model／NVIDIA") || !strings.Contains(text, "尚未取得上游完整明細") || strings.Contains(text, "previously-successful-model") {
		t.Fatal(text, calls)
	}
}
