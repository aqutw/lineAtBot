# lineAtBot

LINE 台股**歷史股價與自然語言對話**：Go + Render Free + OpenRouter `openrouter/free` + SQL。

## 資料與支援範圍

- 預設使用使用者提供 CSV 的 **200 檔股票、193,541 筆**每日開盤、最高、最低、收盤價（OHLC），日期範圍 **2022-08-01～2026-10-14**。
- 來源標示為 `使用者匯入 CSV（非即時行情）`，`demo_data=false`，查價回覆不顯示 Demo／模擬資料字樣。
- 保存「上市半導體」「上市電子零組件」兩個分類、200 筆股票分類關聯，可由 LLM 產生查詢。
- LLM 自行決定是否查詢資料、查哪些股票／日期／OHLC，以及最終回答格式。支援多檔、多日期、開盤／最低／最高／收盤價、統計及一般對話。
- 不再使用固定意圖清單、單檔限制或 Go 行情回覆模板；「說明」、模型身分及非股價問題也交給 LLM。
- 目前每則訊息獨立處理，尚未加入每個使用者的短期對話記憶。
- 工具呼叫只接受 API 正式 `tool_calls`；模型將 XML／工具標記寫入回答時不傳給 LINE，也不執行其中 SQL。協定或內容不完整最多修正一次，每則問題仍最多三次 LLM 呼叫；失敗時告知此次查詢未完成。已嘗試過的相同 SQL 不重複執行。
- SQLite 提供 `price_history` 數值檢視（與 `prices` 同欄位），模型優先使用它做價格排序、比較、統計和 LAG／LEAD；原始十進位字串仍保存在 `prices`。
- 模型流程共用 44 秒預算（包含查詢、限速等待及重試），單次模型呼叫最多 35 秒；可查詢的輪次會優先預留 12 秒供最終回答使用。LINE 從 webhook 收到訊息起總共最多 52 秒，包含排隊，另保留最多 8 秒發送回覆。
- 分別處理上游限流、逾時、認證、額度／預算、權限、參數與回應格式錯誤。429 僅代表限流，不能據此宣稱每日免費額度用完。連 HTTP 200 中的 `error` 也會分類。
- 快速 408／502／503／504 最多重試一次；429 必須有 `Retry-After` 才考慮重試。等待後至少還須有 15 秒預算，所有嘗試仍計入最多三次模型呼叫。認證／參數錯誤及本機取消不盲目重試。
- 不下載行情，股價來源為使用者匯入 CSV。Prompt 要求模型引用查詢結果與實際日期，空結果不得以別日行情冒充。

```text
LINE Webhook → Go 驗證簽章 → 快速回傳 HTTP 200
                  ↓ bounded queue
       OpenRouter LLM：直接回答，或呼叫 SQL 工具
                  ↓（需要資料時）
       唯讀 SQL → SQLite / PostgreSQL → 查詢結果
                  ↓
       OpenRouter LLM 自行撰寫最終回答
                  ↓
       協定檢查通過的原文送到 LINE Reply API（長文只分段）
```

## 本機啟動

需要 Go 1.22+ 與 C compiler（SQLite driver 使用 CGO）；部署使用 Go 1.27.2。

```bash
go test -race ./...
go vet ./...
go build -tags netgo -trimpath -o bin/bot ./cmd/bot
export OPENROUTER_API_KEY='your-key-without-Bearer-prefix'
export OPENROUTER_MODEL='openrouter/free'
export DEMO_API_TOKEN='your-random-test-token'
./bin/bot
```

環境變數格式見 `.env.example`；程式不自動載入 `.env`。密鑰不可提交 Git。

## HTTP endpoints

| Endpoint | 用途 |
| --- | --- |
| `GET /healthz` | DB 狀態、`demo_data` 與 LINE/OpenRouter 是否已設定；不呼叫模型、不揭露密鑰 |
| `POST /line/webhook` | LINE 專用入口，必須驗證 `x-line-signature` |
| `POST /api/query` | 受 Bearer token 保護的整合測試入口；未設定 `DEMO_API_TOKEN` 時回 404 |

```bash
curl http://localhost:10000/healthz
curl http://localhost:10000/api/query \
  -H "Authorization: Bearer $DEMO_API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"text":"台積電最近五筆收盤價"}'
```

`/api/query` 與 LINE 使用相同的 LLM／SQL 工具流程。回傳 `text` 是 LLM 的最終文字，`model` 是 OpenRouter 回傳的模型識別，`queries` 為本次工具結果。行情列建議使用 `stock_name / stock_id / date / open / low / high / close` 欄位。範例日期以匯入資料範圍為準。

Render Logs 可搜尋 `llm_completion`（實際模型、finish reason、工具數、耗時）、`llm_protocol_rejected`（協定錯誤原因）、`llm_tool_result`（查詢狀態）及 `llm_answer_ready`。同一次執行以 `run_id` 串聯，這些診斷不記錄原始模型輸出、SQL、密鑰或 LINE token。既有 `user_prompt` 提問紀錄保留。`/healthz` 的 `tool_protocol_guard=v1` 可確認新版已啟用。

`llm_completion` 在 API 失敗時另外記錄 `http_status`、`upstream_code`、`upstream_error_type`、供應商／模型／request ID（上游有提供時）、`retry_after_ms` 與 `timeout_source`；不記錄 upstream 原始錯誤訊息或 raw body。LINE 的 `event_id` 可與 `user_prompt` 對照。`llm_retry` 記錄重試等待；`line_job_busy`／`line_job_expired` 區分排隊忙碌及期限已過。`/healthz` 的 `llm_recovery=v1` 是本次部署識別。遇到回歸可在 Render Deploys 回滾前一部署；舊版沒有本次詳細錯誤分類。

可測試：「台積電與聯發科在 2026/10/01、2026/10/02 的開高低收，用你覺得清楚的方式列出」、「你是什麼大語言模型？」或「幫我寫一首詩」。

## 真實資料匯入

### 選項 A：目前使用 SQLite snapshot

目前的 `data/` 已由使用者提供的 `*.d.csv` 和 `group_*_stock_name.mapping.csv` 轉換。更新資料時在本機執行：

```bash
python3 scripts/import_wantg.py \
  --input-dir /path/to/wantg --output-dir data
```

工具只讀取來源檔，先驗證全部資料，再產出專案內的 CSV 與 `dataset.json`（包含來源檔 SHA-256、筆數、日期範圍及資料標記）。股票代碼來自檔名、名稱與分類來自 mapping CSV；不匯入 K、D。提交這些輸出並重新部署即可更新線上資料。

把自己的行情轉為下列 CSV，再提交並重新部署：

- `data/stocks.csv`：`code,name`
- `data/prices.csv`：`stock_code,trading_date,open_price,high_price,low_price,close_price`
- `data/groups.csv`：`group_name,stock_code`
- `data/dataset.json`：資料來源及 `demo_data` 標記。SQLite 啟動時必須有有效 metadata。

日期使用 `YYYY-MM-DD`；代碼保留為字串（例如 `0050`）；價格不用千分位逗號，最多 12 位整數、4 位小數（`NUMERIC(16,4)`）。開盤、最高、最低價未知可留空，匯入後為 `NULL`，API 回傳 `null`；收盤價必填。已填價格須非負，且最高價不得低於開盤、最低、收盤價，最低價不得高於開盤或收盤價。

SQLite 每次啟動從嵌入執行檔的 CSV 重建，使用串流讀取與 prepared statements 匯入，隨後以只讀模式開啟。Render Free 的本機檔案不持久，但重新啟動會從同一份已提交資料重建。唯一來源仍應保存在自己的原始 CSV；目前資料標記不因使用 SQLite 而變成 Demo。

### 選項 B：切換 PostgreSQL

1. 在你的 PostgreSQL 以 owner/importer 執行 `migrations/001_postgres.sql`、`migrations/002_add_ohlc.sql` 及 `migrations/003_stock_groups.sql`。既有收盤價資料庫必須在新版程式部署前執行 `002_add_ohlc.sql`，原紀錄的開盤／最高／最低價會保留為 `NULL`。
2. 匯入 `stocks`、`prices`、`stock_groups`、`stock_group_members` 與選用的 `aliases`。
3. 建立只能 SELECT 資料表的 bot 帳號。
4. 在 Render 設定該 bot 帳號的 `DATABASE_URL`，以及 `DATA_SOURCE_LABEL`。

Schema：

```text
stocks(code TEXT PRIMARY KEY, name TEXT UNIQUE NOT NULL)
aliases(alias TEXT PRIMARY KEY, stock_code REFERENCES stocks(code))
stock_groups(name TEXT PRIMARY KEY)
stock_group_members(group_name REFERENCES stock_groups(name),
                    stock_code REFERENCES stocks(code), PRIMARY KEY(group_name,stock_code))
prices(stock_code REFERENCES stocks(code), trading_date DATE,
       open_price NUMERIC(16,4), high_price NUMERIC(16,4), low_price NUMERIC(16,4),
       close_price NUMERIC(16,4), PRIMARY KEY(stock_code,trading_date))
```

若要用 CSV 匯入，可執行：

```bash
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 \
  -f migrations/001_postgres.sql -f migrations/002_add_ohlc.sql \
  -f migrations/003_stock_groups.sql \
  -f scripts/import_postgres.sql
```

匯入時需用 owner/importer 的 URL。匯入目前 CSV 時請設定 `DATA_SOURCE_LABEL=使用者匯入 CSV（非即時行情）`。PostgreSQL 匯入工具會新增或更新同一股票／日期，不會清除遠端資料庫的其他紀錄；目前線上服務仍使用完整替換後的 SQLite snapshot。

外部 DB 連線預設 TLS `sslmode=require`、只讀交易、statement timeout 3 秒。程式不執行遠端 migration。LLM 會收到股票目錄、資料來源及工具查詢結果，以自行組織回答。

## Render Free 部署

使用私有 GitHub repo，將 Render GitHub App 的存取範圍限制為本 repo。

| 設定 | 值 |
| --- | --- |
| Service type | Web Service |
| Name | lineatbot |
| Language | Go |
| Branch | main |
| Region | Singapore |
| Root directory | 留空（repo root 即本專案） |
| Build command | `go build -tags netgo -trimpath -ldflags="-s -w" -o bin/bot ./cmd/bot` |
| Start command | `./bin/bot` |
| Instance | Free |
| Health check path | `/healthz` |

設定 `GO_VERSION=1.27.2`、`CGO_ENABLED=1`、`OPENROUTER_MODEL=openrouter/free`，以及自己的 `OPENROUTER_API_KEY`。

LINE 尚未設定時服務也能啟動；`/healthz` 的 `line_configured=false` 清楚顯示尚待設定。

`render.yaml` 提供相同設定；若使用 Blueprint，密鑰需另行輸入。Free 無流量 15 分鐘會休眠，demo 前先呼叫 `/healthz` 並確認已啟動。不要把冷啟動成功與 LINE reply 成功視為同一件事。

## LINE 設定

1. 建立 LINE Official Account，在 Official Account Manager 啟用 Messaging API。
2. 在 LINE Developers 取得 Channel secret 與 Channel access token，填入 Render 的 `LINE_CHANNEL_SECRET` 與 `LINE_CHANNEL_ACCESS_TOKEN`。
3. Webhook URL 設為 `https://<your-service>.onrender.com/line/webhook`。
4. 按 Verify，啟用 Use webhook、Webhook redelivery，關閉預設自動回覆。
5. 加 bot 好友並詢問「台積電最近五筆收盤價」。

先送最終查價結果；不先消耗 reply token 發送「查詢中」。

## 查看使用者提問

每則簽章驗證成功、接受進佇列的 LINE 文字訊息會寫入應用程式 JSON 日誌，例如：

```json
{"time":"2026-10-14T13:00:00Z","level":"INFO","msg":"user_prompt","channel":"line","event_id":"example-event","prompt":"2330最新股價"}
```

在 Render Dashboard → `lineatbot` → **Logs** 搜尋 `user_prompt`，選擇所需時間範圍或 Live tail，即可查看原始提問。支援範圍外的提問也會記錄；同一事件重送會去重。只記錄文字訊息，不記錄 LINE user ID、reply token、Channel secret 或 API key。紀錄屬於 Render 應用程式日誌，沒有寫入行情資料庫。HTTP `/api/query` 的測試提問不會記錄。

Render Hobby workspace 的日誌目前保留 7 天，超過期限需另行保存；參考 [Render Logs](https://render.com/docs/logging)。

## 限制與驗證

- OpenRouter 使用 function/tool calling 與 `provider.require_parameters=true`，自由路由器須有支援工具的免費模型；不強制最終回答為 JSON。
- 每個 LLM API 呼叫（含工具後續回覆）至少間隔約 3.1 秒，以等待方式控制免費 RPM；不再用「查詢較密集」分支擋掉提問。
- 一般對話通常 1 次模型請求；查庫通常 2 次，查詢修正最多 3 次，因此每日免費請求額度會較快消耗。
- SQL 工具僅接受單一 SELECT/WITH，每次查詢 3 秒、最多 200 列／64 KiB；截斷狀態會交給模型。這些是工具資源保護，不是回覆意圖清單。
- SQLite 以唯讀模式及 authorizer 限制在五張股票資料表，禁止寫入、ATTACH、PRAGMA、系統表與檔案／擴充函數；PostgreSQL 必須使用專用唯讀帳號，工具另開只讀交易。
- 模型單次輸出預算為 4096 tokens。Go 保留最終文字，LINE 傳輸上按平台限制拆為最多 5 則、每則 5000 UTF-16 units，不套行情模板。
- LINE 佇列工作有 52 秒處理預算，保留 8 秒給 Reply API；模型／網路／額度失敗仍需回傳服務錯誤訊息。
- Webhook 重送去重保留 20 分鐘，狀態在記憶體；Render 重啟後不保留。Reply API 的 token 單次使用限制仍生效。
- 非同步佇列也是記憶體，程序重啟可能丟失已 ACK 的工作；正式上線需持久化佇列／去重紀錄。
- 不自動重試 HTTP 模型或 Reply API 請求；LLM 可以針對工具 SQL 錯誤修正查詢。
- 測試覆蓋 LLM 原文轉送、一般問題／說明、多檔多日 OHLC、工具錯誤交回模型、SQL 保護、LINE 簽章／去重／長文分段，以及受保護 HTTP API。
- 小型模擬行情僅保留在 `internal/testfixture/` 供測試使用，不會作為正式執行檔的資料集。完整匯入資料另有筆數、分類、OHLC 與資料標記測試。

官方文件：[Render Free](https://render.com/docs/free)、[LINE Webhook](https://developers.line.biz/en/docs/messaging-api/building-bot/)、[OpenRouter tool calling](https://openrouter.ai/docs/guides/features/tool-calling)。
