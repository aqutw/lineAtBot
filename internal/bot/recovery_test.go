package bot

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/aqutw/lineAtBot/internal/openrouter"
)

func TestRecoveryRetriesFast504ThenQueriesAndAnswers(t *testing.T) {
	s := serviceFixture(t)
	var logs bytes.Buffer
	s.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	calls := 0
	s.Model = modelFunc(func(ctx context.Context, m []openrouter.Message, _ []openrouter.Tool, choice string) (openrouter.Completion, error) {
		calls++
		if calls == 1 {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) <= 20*time.Second || time.Until(deadline) > 33*time.Second {
				t.Fatal("first call must leave time for final answer", deadline)
			}
			return openrouter.Completion{}, &openrouter.APIError{Kind: openrouter.Timeout, HTTPStatus: 504, Code: 504, Provider: "NVIDIA", RequestID: "request-failed", TimeoutSource: "upstream", Cause: openrouter.ErrUnavailable}
		}
		if calls == 2 {
			return openrouter.Completion{Message: openrouter.Message{ToolCalls: []openrouter.ToolCall{queryCall("data", "SELECT COUNT(*) AS n FROM price_history")}}}, nil
		}
		if m[len(m)-1].Role != "tool" || choice != "none" {
			t.Fatal("missing result", m)
		}
		return openrouter.Completion{Model: "free-model", Message: openrouter.Message{Content: "查到30筆。"}}, nil
	})
	answer := s.Answer(WithRequestID(context.Background(), "line-event-123"), "資料筆數")
	if answer.Text != "查到30筆。" || calls != 3 || len(answer.Queries) != 1 {
		t.Fatal(answer, calls)
	}
	for _, text := range []string{`"msg":"llm_retry"`, `"event_id":"line-event-123"`, `"http_status":504`, `"provider":"NVIDIA"`, `"upstream_request_id":"request-failed"`} {
		if !strings.Contains(logs.String(), text) {
			t.Fatal("missing diagnostic", text, logs.String())
		}
	}
}

func TestRateLimitDoesNotClaimDailyQuotaIsExhaustedOrBlindlyRetry(t *testing.T) {
	for _, retryAfter := range []time.Duration{0, time.Minute} {
		s := serviceFixture(t)
		calls := 0
		s.Model = modelFunc(func(context.Context, []openrouter.Message, []openrouter.Tool, string) (openrouter.Completion, error) {
			calls++
			return openrouter.Completion{}, &openrouter.APIError{Kind: openrouter.RateLimited, HTTPStatus: 429, RetryAfter: retryAfter, Cause: openrouter.ErrRateLimited}
		})
		answer := s.Answer(context.Background(), "查價")
		if calls != 1 || !strings.Contains(answer.Text, "不一定代表每日免費額度已用完") {
			t.Fatal(answer, calls)
		}
	}
}

func TestRetriesNeverExceedOneOrOutliveBudget(t *testing.T) {
	for _, budget := range []time.Duration{time.Minute, 100 * time.Millisecond} {
		s := serviceFixture(t)
		calls := 0
		s.Model = modelFunc(func(context.Context, []openrouter.Message, []openrouter.Tool, string) (openrouter.Completion, error) {
			calls++
			return openrouter.Completion{}, &openrouter.APIError{Kind: openrouter.Unavailable, HTTPStatus: 503, Code: 503, Cause: openrouter.ErrUnavailable}
		})
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		answer := s.Answer(ctx, "查價")
		cancel()
		want := 1
		if budget > minRetryBudget {
			want = 2
		}
		if calls != want || !strings.Contains(answer.Text, "上游供應商") {
			t.Fatal(answer, calls, want)
		}
	}
}

func TestModelErrorRepliesAreDistinctAndPermanentFailuresDoNotRetry(t *testing.T) {
	for _, tc := range []struct {
		kind openrouter.ErrorKind
		text string
	}{
		{openrouter.Authentication, "認證異常"}, {openrouter.Billing, "預算限制"}, {openrouter.Forbidden, "拒絕"},
		{openrouter.BadRequest, "參數不相容"}, {openrouter.InvalidResponse, "格式異常"}, {openrouter.Configuration, "連線設定"},
	} {
		s := serviceFixture(t)
		calls := 0
		s.Model = modelFunc(func(context.Context, []openrouter.Message, []openrouter.Tool, string) (openrouter.Completion, error) {
			calls++
			return openrouter.Completion{}, &openrouter.APIError{Kind: tc.kind, Cause: openrouter.ErrUnavailable}
		})
		answer := s.Answer(context.Background(), "查詢")
		if calls != 1 || !strings.Contains(answer.Text, tc.text) {
			t.Fatal(tc.kind, answer, calls)
		}
	}
	if !strings.Contains(modelFailureReply(context.DeadlineExceeded), "等待逾時") {
		t.Fatal("deadline mapping")
	}
}

func TestModelCancellationStillProducesUserReply(t *testing.T) {
	s := serviceFixture(t)
	s.Model = modelFunc(func(ctx context.Context, _ []openrouter.Message, _ []openrouter.Tool, _ string) (openrouter.Completion, error) {
		<-ctx.Done()
		return openrouter.Completion{}, ctx.Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	answer := s.Answer(ctx, "查價")
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) || !strings.Contains(answer.Text, "等待逾時") {
		t.Fatal(answer)
	}
}
