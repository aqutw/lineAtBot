package bot

import (
	"fmt"
	"strings"

	"github.com/aqutw/lineAtBot/internal/openrouter"
)

// Build failure replies in Go using safe metadata and fixed reason mappings.
// No LLM call is needed during an outage and no upstream message is echoed.
func modelFailureReply(err error, earlier ...error) string {
	var lines []string
	seen := make(map[string]bool)
	for _, failure := range append(earlier, err) {
		for _, attempt := range openrouter.FailureAttempts(failure) {
			label := attempt.Model
			if label == "" {
				label = "模型型號未提供"
			}
			if attempt.Provider != "" {
				label += "／" + attempt.Provider
			}
			line := label + "：" + attemptReason(attempt)
			if attempt.Code != 0 {
				line += fmt.Sprintf("（%d）", attempt.Code)
			}
			if !seen[line] {
				seen[line] = true
				lines = append(lines, "• "+line)
			}
		}
	}
	note := "僅列已取得的失敗資訊，可能不含完整上游嘗試紀錄。"
	if len(lines) > 6 {
		lines = lines[:6]
		note = "失敗資訊較多，僅顯示前 6 項；可能不含完整上游嘗試紀錄。"
	}
	return modelFailureSummary(err) + "\n\n已知失敗資訊：\n" + strings.Join(lines, "\n") + "\n" + note
}

func attemptReason(a openrouter.Attempt) string {
	// Typed codes add detail without quoting arbitrary upstream descriptions.
	switch a.ErrorType {
	case "provider_overloaded":
		return "供應商負載過高，暫時忙碌"
	case "context_length_exceeded", "string_too_long", "payload_too_large":
		return "輸入內容超過模型可接受長度"
	case "max_tokens_exceeded":
		return "模型輸出達長度上限，未完成回答"
	case "content_policy_violation", "refusal":
		return "請求受到內容政策限制"
	}
	switch a.Kind {
	case openrouter.Timeout:
		if a.TimeoutSource == "client_deadline" || a.TimeoutSource == "transport" {
			return "等待回應逾時，尚未取得上游完整明細"
		}
		return "回應逾時"
	case openrouter.Cancelled:
		return "請求已中止"
	case openrouter.RateLimited:
		return "請求受限，暫時無法處理更多請求"
	case openrouter.Authentication, openrouter.Configuration:
		return "連線設定或認證異常"
	case openrouter.Billing:
		return "帳號額度或預算受限"
	case openrouter.Forbidden:
		return "請求被拒絕，需檢查權限或服務限制"
	case openrouter.BadRequest:
		return "請求格式或參數不相容"
	case openrouter.InvalidResponse:
		return "回覆內容不完整或格式異常"
	default:
		return "服務暫時無法提供回覆，確切原因未提供"
	}
}
