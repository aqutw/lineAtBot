package bot

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aqutw/lineAtBot/internal/openrouter"
	"github.com/aqutw/lineAtBot/internal/stock"
)

const Help = "台股收盤價查詢\n\n可以問我：\n・台積電最近收盤多少？\n・2330 在 2026/10/01 收盤多少？\n・聯發科最近五個交易日的收盤價\n\n一次查一檔，歷史查詢最多 20 筆。只回答已入庫的收盤行情，會附資料日期與來源。"

type Parser interface {
	Parse(context.Context, string, time.Time) (stock.Query, error)
}
type Service struct {
	Store  *stock.Store
	Parser Parser
	Now    func() time.Time
}
type Answer struct {
	Text   string        `json:"text"`
	Query  *stock.Query  `json:"query,omitempty"`
	Result *stock.Result `json:"result,omitempty"`
}

func (s *Service) Answer(ctx context.Context, text string) Answer {
	now := s.Now()
	text = strings.TrimSpace(text)
	if text == "" || len([]rune(text)) > 500 {
		return Answer{Text: "請用 500 字以內描述股票、日期及查詢需求。"}
	}
	if text == "help" || text == "說明" || text == "幫助" || text == "使用說明" {
		return Answer{Text: Help}
	}
	q, err := s.Parser.Parse(ctx, text, now)
	if err != nil {
		if errors.Is(err, openrouter.ErrRateLimited) {
			return Answer{Text: "免費模型目前已達請求上限，請稍後再試。"}
		}
		return Answer{Text: "目前無法完成語意解析，請稍後再試，或輸入「說明」查看支援的問法。"}
	}
	answer := Answer{Query: &q}
	switch q.Intent {
	case "help":
		answer.Text = Help
		return answer
	case "clarify":
		answer.Text = "請補充一檔股票名稱或代號，以及日期／最近幾個交易日（最多 20）。"
		return answer
	case "unsupported":
		answer.Text = "目前支援最新、指定日期與區間收盤價查詢。請輸入「說明」查看範例。"
		return answer
	}
	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	result, err := s.Store.Query(queryCtx, q, now)
	if errors.Is(err, sql.ErrNoRows) {
		answer.Text = "資料庫找不到這檔股票，請確認名稱或代號。"
		return answer
	}
	if err != nil {
		answer.Text = "目前無法查詢資料庫，請稍後再試。"
		return answer
	}
	answer.Result = &result
	answer.Text = Format(q, result)
	return answer
}

func Format(q stock.Query, result stock.Result) string {
	var out strings.Builder
	if result.Demo {
		out.WriteString("【DEMO 模擬資料・非真實行情】\n")
	}
	fmt.Fprintf(&out, "%s（%s）\n", result.Stock.Name, result.Stock.Code)
	if len(result.Prices) == 0 {
		if q.Intent == "price_on_date" {
			fmt.Fprintf(&out, "%s 沒有已入庫的收盤行情。\n", q.Date)
		} else {
			out.WriteString("指定區間沒有已入庫的收盤行情。\n")
		}
		if result.LatestDate != "" {
			fmt.Fprintf(&out, "資料庫最新交易日：%s\n", result.LatestDate)
		}
	} else {
		for _, p := range result.Prices {
			fmt.Fprintf(&out, "%s｜收盤 %s 元\n", p.Date, p.Close)
		}
		if q.Intent == "recent_history" && len(result.Prices) < q.Limit {
			fmt.Fprintf(&out, "要求 %d 筆，目前僅有 %d 筆資料。\n", q.Limit, len(result.Prices))
		}
	}
	fmt.Fprintf(&out, "資料來源：%s", result.Source)
	return out.String()
}
