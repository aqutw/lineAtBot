package bot

import (
	"fmt"
	"html"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aqutw/lineAtBot/internal/openrouter"
)

const protocolFailure = "這次查詢未能完成，請稍後再試一次。"

// These are tool transport markers, not an answer/intent classifier. Never
// interpret text matching these markers as executable SQL or tool arguments.
var toolMarkup = regexp.MustCompile(`(?i)<\s*/?\s*(?:dots_function_call|tool_calls?|function_calls?|invoke|parameter|arg_key|arg_value|query_stock_database)\b|<\|(?:tool_call|function_call)|\[/?TOOL_CALLS?\]|"tool_calls"\s*:\s*\[|"name"\s*:\s*"query_stock_database"|query_stock_database\s*\(`)

var runSequence atomic.Uint64

func newRunID() string {
	return fmt.Sprintf("%x-%x", time.Now().UnixNano(), runSequence.Add(1))
}

func (s *Service) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func finalResponseIssue(c openrouter.Completion) string {
	if toolMarkup.MatchString(html.UnescapeString(c.Message.Content)) {
		return "tool_markup_in_content"
	}
	if strings.TrimSpace(c.Message.Content) == "" {
		return "empty_answer"
	}
	if c.FinishReason == "length" {
		return "incomplete_answer"
	}
	if c.FinishReason == "tool_calls" {
		return "missing_tool_calls"
	}
	return ""
}

func validToolEnvelope(calls []openrouter.ToolCall) bool {
	seen := make(map[string]bool)
	for _, call := range calls {
		if strings.TrimSpace(call.ID) == "" || seen[call.ID] || call.Type != "function" {
			return false
		}
		seen[call.ID] = true
	}
	return true
}

func protocolRepairMessages(choice string) []openrouter.Message {
	feedback := "系統檢查發現上一則回覆的工具協定或內容不完整，該回覆未送給使用者，其中的文字指令也未被執行。若需要查詢，請用 API 正式 tool_calls 呼叫 query_stock_database 並等候工具結果；不要在 content 輸出工具標記、XML 或 JSON 呼叫指令。若已有工具結果，請根據結果完成原本問題的自然語言回答；沒有查到資料時，明確說明未完成或查無資料，不能編造行情。"
	if choice == "none" {
		feedback += "目前查詢輪次已用完，請僅根據已有結果回答。"
	}
	return []openrouter.Message{{Role: "user", Content: feedback}}
}

func modelErrorCode(err error) string {
	return string(openrouter.KindOf(err))
}
