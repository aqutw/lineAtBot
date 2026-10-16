package bot

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/aqutw/lineAtBot/internal/openrouter"
)

const leakedCall = `<dots_function_call>
<invoke name="query_stock_database"><parameter name="sql">
DELETE FROM prices;
</parameter></invoke></dots_function_call>`

func queryCall(id, sql string) openrouter.ToolCall {
	call := openrouter.ToolCall{ID: id, Type: "function"}
	call.Function.Name = "query_stock_database"
	args, _ := json.Marshal(map[string]string{"sql": sql})
	call.Function.Arguments = string(args)
	return call
}

func TestTextToolCallIsNotExecutedOrSentAndCanRecover(t *testing.T) {
	s := serviceFixture(t)
	var logs bytes.Buffer
	s.Log = slog.New(slog.NewJSONHandler(&logs, nil))
	calls := 0
	final := "台積電（2330）目前資料共六筆；這是模型自行撰寫的答案。"
	s.Model = modelFunc(func(_ context.Context, messages []openrouter.Message, _ []openrouter.Tool, choice string) (openrouter.Completion, error) {
		calls++
		switch calls {
		case 1:
			return openrouter.Completion{Model: "routed-model", FinishReason: "stop", Message: openrouter.Message{Content: leakedCall}}, nil
		case 2:
			last := messages[len(messages)-1]
			if !strings.Contains(last.Content, "正式 tool_calls") || choice != "auto" {
				t.Fatal("missing correction", last, choice)
			}
			for _, m := range messages {
				if strings.Contains(m.Content, "DELETE FROM") {
					t.Fatal("invalid content must not be replayed as executable instructions")
				}
			}
			return openrouter.Completion{Message: openrouter.Message{ToolCalls: []openrouter.ToolCall{queryCall("actual", "SELECT COUNT(*) AS n FROM price_history WHERE stock_code='2330'")}}}, nil
		default:
			last := messages[len(messages)-1]
			if last.Role != "tool" || !strings.Contains(last.Content, `"n":6`) || choice != "none" {
				t.Fatal("missing actual database evidence", last, choice)
			}
			return openrouter.Completion{Message: openrouter.Message{Content: final}}, nil
		}
	})
	answer := s.Answer(context.Background(), "2330有幾筆歷史資料？")
	if answer.Text != final || calls != 3 || len(answer.Queries) != 1 {
		t.Fatalf("%+v calls=%d", answer, calls)
	}
	stats, err := s.Store.Stats(context.Background())
	if err != nil || stats.Prices != 30 {
		t.Fatal("data changed", stats, err)
	}
	if !strings.Contains(logs.String(), `"reason":"tool_markup_in_content"`) || !strings.Contains(logs.String(), `"model":"routed-model"`) || strings.Contains(logs.String(), "DELETE FROM") || strings.Contains(logs.String(), "dots_function_call") {
		t.Fatal("diagnostics must contain metadata, not raw leaked content", logs.String())
	}
}

func TestPersistentProtocolFailureStopsAfterOneRepair(t *testing.T) {
	for _, content := range []string{leakedCall, "&lt;dots_function_call&gt;broken", "<tool_call>{}", `[TOOL_CALLS] []`, `{"tool_calls":[]}`, `{"name":"query_stock_database","arguments":{"sql":"SELECT 1"}}`, "<query_stock_database>SELECT 1</query_stock_database>", "query_stock_database({})", ""} {
		t.Run(content, func(t *testing.T) {
			s := serviceFixture(t)
			calls := 0
			s.Model = modelFunc(func(context.Context, []openrouter.Message, []openrouter.Tool, string) (openrouter.Completion, error) {
				calls++
				return openrouter.Completion{Message: openrouter.Message{Content: content}}, nil
			})
			answer := s.Answer(context.Background(), "請查詢股價")
			if answer.Text != protocolFailure || calls != 2 || len(answer.Queries) != 0 {
				t.Fatalf("%+v calls=%d", answer, calls)
			}
		})
	}
}

func TestFinalProtocolLeakAfterQueriesNeverReachesUser(t *testing.T) {
	s := serviceFixture(t)
	calls := 0
	s.Model = modelFunc(func(context.Context, []openrouter.Message, []openrouter.Tool, string) (openrouter.Completion, error) {
		calls++
		if calls < 3 {
			return openrouter.Completion{Message: openrouter.Message{ToolCalls: []openrouter.ToolCall{queryCall("query", "SELECT '"+strings.Repeat("x", calls)+"' AS value")}}}, nil
		}
		return openrouter.Completion{Message: openrouter.Message{Content: leakedCall}}, nil
	})
	answer := s.Answer(context.Background(), "查價")
	if answer.Text != protocolFailure || calls != 3 {
		t.Fatalf("%+v calls=%d", answer, calls)
	}
}

func TestInvalidToolEnvelopeCanRecoverWithoutSendingInvalidHistory(t *testing.T) {
	for _, kind := range []string{"missing_id", "duplicate_id", "wrong_type"} {
		t.Run(kind, func(t *testing.T) {
			s := serviceFixture(t)
			calls := 0
			s.Model = modelFunc(func(_ context.Context, m []openrouter.Message, _ []openrouter.Tool, _ string) (openrouter.Completion, error) {
				calls++
				if calls == 1 {
					call := queryCall("q", "SELECT 1")
					toolCalls := []openrouter.ToolCall{call}
					switch kind {
					case "missing_id":
						toolCalls[0].ID = ""
					case "duplicate_id":
						toolCalls = append(toolCalls, call)
					case "wrong_type":
						toolCalls[0].Type = "unknown"
					}
					return openrouter.Completion{Message: openrouter.Message{ToolCalls: toolCalls}}, nil
				}
				for _, message := range m {
					if len(message.ToolCalls) != 0 || message.Role == "tool" {
						t.Fatal("invalid envelope replayed", m)
					}
				}
				return openrouter.Completion{Message: openrouter.Message{Content: "這次查詢未執行。"}}, nil
			})
			if answer := s.Answer(context.Background(), "查詢"); answer.Text != "這次查詢未執行。" || calls != 2 || len(answer.Queries) != 0 {
				t.Fatal(answer, calls)
			}
		})
	}
}

func TestRepeatedSQLGetsFeedbackInsteadOfAnotherDatabaseExecution(t *testing.T) {
	s := serviceFixture(t)
	calls := 0
	s.Model = modelFunc(func(_ context.Context, m []openrouter.Message, _ []openrouter.Tool, _ string) (openrouter.Completion, error) {
		calls++
		if calls < 3 {
			return openrouter.Completion{Message: openrouter.Message{ToolCalls: []openrouter.ToolCall{queryCall("q", "SELECT COUNT(*) AS n FROM prices")}}}, nil
		}
		if !strings.Contains(m[len(m)-1].Content, "duplicate_query") {
			t.Fatal(m)
		}
		return openrouter.Completion{Message: openrouter.Message{Content: "資料共30筆。"}}, nil
	})
	answer := s.Answer(context.Background(), "資料筆數")
	if answer.Text != "資料共30筆。" || len(answer.Queries) != 1 || calls != 3 {
		t.Fatal(answer, calls)
	}
}

func TestIncompleteAnswerIsRepairedWithoutLeakingPartialText(t *testing.T) {
	s := serviceFixture(t)
	calls := 0
	s.Model = modelFunc(func(context.Context, []openrouter.Message, []openrouter.Tool, string) (openrouter.Completion, error) {
		calls++
		if calls == 1 {
			return openrouter.Completion{FinishReason: "length", Message: openrouter.Message{Content: "未完成的文字"}}, nil
		}
		return openrouter.Completion{Message: openrouter.Message{Content: "完整回答。"}}, nil
	})
	if answer := s.Answer(context.Background(), "你好"); answer.Text != "完整回答。" || calls != 2 {
		t.Fatal(answer, calls)
	}
}
