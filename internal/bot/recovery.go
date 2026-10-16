package bot

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/aqutw/lineAtBot/internal/openrouter"
)

const AnswerBudget = 44 * time.Second
const finalAnswerReserve = 12 * time.Second
const minRetryBudget = 15 * time.Second

func remaining(ctx context.Context) time.Duration {
	if deadline, ok := ctx.Deadline(); ok {
		return time.Until(deadline)
	}
	return AnswerBudget
}

// Leave time for the final model turn after a tool call. All turns and retries
// still inherit the one overall deadline; the LINE transport reserves another 8s.
func callBudget(ctx context.Context, choice string) time.Duration {
	budget := remaining(ctx)
	if choice != "none" && budget > finalAnswerReserve+8*time.Second {
		budget -= finalAnswerReserve
	}
	return min(budget, openrouter.MaxCallTimeout)
}

func modelFailureSummary(err error) string {
	switch openrouter.KindOf(err) {
	case openrouter.RateLimited:
		return "目前模型服務或供應商正在限流，請稍後再試。這不一定代表每日免費額度已用完。"
	case openrouter.Timeout:
		return "這次模型回覆等待逾時，查詢未完成，請稍後再試。"
	case openrouter.Cancelled:
		return "這次模型請求已中止，查詢未完成，請稍後再試。"
	case openrouter.Authentication, openrouter.Configuration:
		return "目前模型服務的連線設定或認證異常，需要管理員檢查。"
	case openrouter.Billing:
		return "目前模型服務受到帳號額度或預算限制，需要管理員檢查。"
	case openrouter.Forbidden:
		return "這次請求被模型服務拒絕，需要管理員檢查權限或服務限制。"
	case openrouter.BadRequest:
		return "目前模型請求格式或參數不相容，需要管理員檢查。"
	case openrouter.InvalidResponse:
		return "模型回傳的內容不完整或格式異常，這次查詢未完成，請稍後再試。"
	default:
		return "目前無法連線模型服務，或上游供應商暫時無法提供回覆，請稍後再試。"
	}
}

func logCompletion(log *slog.Logger, c openrouter.Completion, err error, turn int, elapsed time.Duration) {
	attrs := []any{"turn", turn, "model", c.Model, "request_id", c.RequestID, "finish_reason", c.FinishReason,
		"tool_calls", len(c.Message.ToolCalls), "latency_ms", elapsed.Milliseconds(), "error_code", modelErrorCode(err)}
	if len(c.FailedAttempts) > 0 {
		attrs = append(attrs, "failed_attempts", openrouter.FailureAttempts(&openrouter.APIError{Attempts: c.FailedAttempts}))
	}
	var api *openrouter.APIError
	if errors.As(err, &api) {
		attrs = append(attrs, "http_status", api.HTTPStatus, "upstream_code", api.Code, "upstream_error_type", api.ErrorType,
			"upstream_request_id", api.RequestID, "provider", api.Provider, "upstream_model", api.Model,
			"retry_after_ms", api.RetryAfter.Milliseconds(), "timeout_source", api.TimeoutSource)
		attrs = append(attrs, "failed_attempts", openrouter.FailureAttempts(err))
	}
	log.Info("llm_completion", attrs...)
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type requestIDKey struct{}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, id)
}
func requestID(ctx context.Context) string { id, _ := ctx.Value(requestIDKey{}).(string); return id }
