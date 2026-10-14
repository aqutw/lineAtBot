# lineAtBot

LINE 台股**歷史收盤價** demo：Go + Render Free + OpenRouter `openrouter/free` + SQL。

## 資料與支援範圍

- 預設使用 5 檔股票、30 筆**模擬**每日開盤、最高、最低、收盤價（OHLC），只用於驗證串接；每則行情回覆都會標示 `DEMO 模擬資料・非真實行情`。
- 支援資料庫最新一筆、指定日期、最近 1–20 筆，以及最長 90 天區間（最多回傳 60 筆）。
- 一次一檔；價格一律為收盤價。不提供成交量、盤中即時價格或投資建議。
- 資料庫及 `/api/query` 的 `result.prices` 已包含 `open`、`high`、`low`、`close`；LINE 的自然語言查詢與文字回覆目前仍只提供收盤價。
- 不下載行情。真實資料由使用者匯入。
- 今天沒有資料時回覆「沒有已入庫的收盤行情」，不自動拿其他日期冒充。

```text
LINE Webhook → Go 驗證原始 body 簽章 → 快速回傳 HTTP 200
                  ↓ bounded queue
         OpenRouter 解析查詢條件 JSON
                  ↓ 本機驗證
         Go 固定 SQL + 綁定參數
                  ↓
         SQLite demo 或 PostgreSQL
                  ↓
         Go 格式化 → LINE Reply API
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
| `GET /healthz` | DB 狀態、模擬資料標示與 LINE/OpenRouter 是否已設定；不呼叫模型、不揭露密鑰 |
| `POST /line/webhook` | LINE 專用入口，必須驗證 `x-line-signature` |
| `POST /api/query` | 受 Bearer token 保護的整合測試入口；未設定 `DEMO_API_TOKEN` 時回 404 |

```bash
curl http://localhost:10000/healthz
curl http://localhost:10000/api/query \
  -H "Authorization: Bearer $DEMO_API_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"text":"台積電最近五筆收盤價"}'
```

`/api/query` 與 LINE 使用相同解析、驗證、SQL 與回覆邏輯。範例日期以模擬資料範圍為準。

## 真實資料匯入

### 選項 A：先維持 SQLite demo

把自己的行情轉為下列 CSV，再提交並重新部署：

- `data/stocks.csv`：`code,name`
- `data/prices.csv`：`stock_code,trading_date,open_price,high_price,low_price,close_price`

日期使用 `YYYY-MM-DD`；代碼保留為字串（例如 `0050`）；價格不用千分位逗號，最多 12 位整數、4 位小數（`NUMERIC(16,4)`）。開盤、最高、最低價未知可留空，匯入後為 `NULL`，API 回傳 `null`；收盤價必填。已填價格須非負，且最高價不得低於開盤、最低、收盤價，最低價不得高於開盤或收盤價。Demo 的 OHLC 全是模擬值。

SQLite 每次啟動從 CSV 重建，隨後以只讀模式開啟。Render Free 的本機檔案不持久；不可把唯一資料存入容器中的 DB。SQLite 模式仍會保留 DEMO 標示。

### 選項 B：切換 PostgreSQL

1. 在你的 PostgreSQL 以 owner/importer 執行 `migrations/001_postgres.sql` 及 `migrations/002_add_ohlc.sql`。既有收盤價資料庫必須在新版程式部署前執行 `002_add_ohlc.sql`，原紀錄的開盤／最高／最低價會保留為 `NULL`。
2. 匯入自己準備的 `stocks`、`prices` 與選用的 `aliases`。
3. 建立只能 SELECT 這三張表的 bot 帳號。
4. 在 Render 設定該 bot 帳號的 `DATABASE_URL`，以及 `DATA_SOURCE_LABEL`。

Schema：

```text
stocks(code TEXT PRIMARY KEY, name TEXT UNIQUE NOT NULL)
aliases(alias TEXT PRIMARY KEY, stock_code REFERENCES stocks(code))
prices(stock_code REFERENCES stocks(code), trading_date DATE,
       open_price NUMERIC(16,4), high_price NUMERIC(16,4), low_price NUMERIC(16,4),
       close_price NUMERIC(16,4), PRIMARY KEY(stock_code,trading_date))
```

若要用 CSV 匯入，可執行：

```bash
psql "$DATABASE_URL" -v ON_ERROR_STOP=1 \
  -f migrations/001_postgres.sql -f migrations/002_add_ohlc.sql \
  -f scripts/import_postgres.sql
```

匯入時需用 owner/importer 的 URL。內建 CSV 是模擬資料，匯入它們時請設定 `DATA_SOURCE_LABEL=DEMO 模擬資料（非真實行情）`。

外部 DB 連線預設 TLS `sslmode=require`、只讀交易、statement timeout 3 秒。程式不執行遠端 migration，也不會把資料庫中的股價傳給模型。

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

- 使用 JSON Schema 與 `provider.require_parameters=true`；free router 無可用支援端點時會回覆暫時無法解析。
- 每個程序限制模型呼叫間隔約 3.1 秒，避免超過每分鐘 20 次；每日額度仍由 OpenRouter 控制。
- Webhook 重送去重保留 20 分鐘，狀態在記憶體；Render 重啟後不保留。Reply API 的 token 單次使用限制仍生效。
- 非同步佇列也是記憶體，程序重啟可能丟失已 ACK 的工作；這是單執行個體 demo，正式上線需持久化佇列/去重紀錄。
- 不自動重試模型或 Reply API，避免多消耗免費額度或重複使用 token。
- 本機測試覆蓋簽章、空事件驗證、重送去重、參數化查詢、只讀 DB、日期/筆數限制、無資料不回退、模型輸出驗證，以及受保護 API 的完整查價流程。測試使用模擬模型與 LINE，不消耗 API 額度。

官方文件：[Render Free](https://render.com/docs/free)、[LINE Webhook](https://developers.line.biz/en/docs/messaging-api/building-bot/)、[OpenRouter structured outputs](https://openrouter.ai/docs/guides/features/structured-outputs)。
