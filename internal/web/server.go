package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aqutw/lineAtBot/internal/bot"
	"github.com/aqutw/lineAtBot/internal/line"
)

type Replier interface {
	Reply(context.Context, string, string) error
}
type loadingIndicator interface {
	ShowLoading(context.Context, string, int) error
}
type Config struct {
	Secret       string
	LINEEnabled  bool
	ModelEnabled bool
	DemoToken    string
}
type job struct {
	event    line.Event
	received time.Time
}
type Server struct {
	Bot    *bot.Service
	LINE   Replier
	Config Config
	Log    *slog.Logger
	ctx    context.Context
	queue  chan job
	mu     sync.Mutex
	seen   map[string]time.Time
	wg     sync.WaitGroup
}

func New(ctx context.Context, service *bot.Service, replier Replier, cfg Config, logger *slog.Logger) *Server {
	s := &Server{Bot: service, LINE: replier, Config: cfg, Log: logger, ctx: ctx, queue: make(chan job, 16), seen: make(map[string]time.Time)}
	s.wg.Add(1)
	go s.run()
	return s
}
func (s *Server) Wait() { s.wg.Wait() }
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /line/webhook", s.webhook)
	mux.HandleFunc("POST /api/query", s.query)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><html lang="zh-Hant"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>lineAtBot</title><body><main><h1>lineAtBot</h1><p>台股收盤價查詢服務</p><p>查詢已入庫的歷史收盤價，資料來源以查詢回覆為準。</p><a href="/healthz">查看服務狀態</a></main></body></html>`)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		mux.ServeHTTP(w, r)
	})
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value)
}
func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	stats, err := s.Bot.Store.Stats(ctx)
	if err != nil {
		respond(w, 503, map[string]string{"status": "database_unavailable"})
		return
	}
	respond(w, 200, map[string]any{"status": "ok", "database": stats, "demo_data": s.Bot.Store.Demo,
		"line_configured": s.Config.LINEEnabled, "openrouter_configured": s.Config.ModelEnabled,
		"response_mode": "llm_with_readonly_sql", "tool_protocol_guard": "v1", "llm_recovery": "v1", "loading_indicator": "v1", "failure_details": "v1"})
}
func eventID(e line.Event) string {
	if e.ID != "" {
		return e.ID
	}
	h := sha256.Sum256([]byte(e.ReplyToken))
	return hex.EncodeToString(h[:])
}
func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	if s.Config.Secret == "" {
		respond(w, 503, map[string]string{"error": "line_not_configured"})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024*1024))
	if err != nil {
		respond(w, 413, map[string]string{"error": "body_too_large"})
		return
	}
	if !line.VerifySignature(body, r.Header.Get("X-Line-Signature"), s.Config.Secret) {
		respond(w, 403, map[string]string{"error": "invalid_signature"})
		return
	}
	var payload struct {
		Events []line.Event `json:"events"`
	}
	if json.Unmarshal(body, &payload) != nil {
		respond(w, 400, map[string]string{"error": "invalid_json"})
		return
	}
	if len(payload.Events) > 100 {
		respond(w, 400, map[string]string{"error": "too_many_events"})
		return
	}
	if len(payload.Events) == 0 {
		respond(w, 200, map[string]bool{"ok": true})
		return
	}
	if !s.Config.LINEEnabled {
		respond(w, 503, map[string]string{"error": "line_not_configured"})
		return
	}
	s.mu.Lock()
	now := time.Now()
	for key, at := range s.seen {
		if now.Sub(at) > 20*time.Minute {
			delete(s.seen, key)
		}
	}
	batch := make(map[string]bool)
	var jobs []job
	for _, e := range payload.Events {
		if e.Type != "message" || e.Message.Type != "text" || e.ReplyToken == "" {
			continue
		}
		key := eventID(e)
		if _, ok := s.seen[key]; ok {
			continue
		}
		if batch[key] {
			continue
		}
		batch[key] = true
		jobs = append(jobs, job{event: e, received: now})
	}
	if len(s.seen)+len(jobs) > 2048 || len(s.queue)+len(jobs) > cap(s.queue) {
		s.mu.Unlock()
		w.Header().Set("Retry-After", "5")
		respond(w, 503, map[string]string{"error": "busy"})
		return
	}
	for _, j := range jobs {
		s.seen[eventID(j.event)] = now
		s.queue <- j
		s.Log.Info("user_prompt", "channel", "line", "event_id", eventID(j.event), "prompt", j.event.Message.Text)
	}
	s.mu.Unlock()
	respond(w, 200, map[string]bool{"ok": true})
}
func (s *Server) answer(ctx context.Context, text string) bot.Answer {
	return s.Bot.Answer(ctx, text)
}
func (s *Server) run() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case j := <-s.queue:
			s.process(j)
		}
	}
}

func (s *Server) process(j job) {
	deadline := j.received.Add(52 * time.Second)
	remaining := time.Until(deadline)
	if remaining <= 0 {
		s.Log.Warn("line_job_expired", "event_id", eventID(j.event))
		return
	}
	text := "目前服務忙碌，這次查詢尚未處理，請稍後重新傳送。"
	if remaining > 10*time.Second {
		ctx, cancel := context.WithDeadline(bot.WithRequestID(s.ctx, eventID(j.event)), deadline.Add(-8*time.Second))
		s.showLoading(ctx, j.event)
		text = s.answer(ctx, j.event.Message.Text).Text
		cancel()
	} else {
		s.Log.Warn("line_job_busy", "event_id", eventID(j.event))
	}
	// A fresh context preserves error replies after the model budget expires,
	// while this absolute deadline also includes any time already spent queued.
	replyCtx, cancel := context.WithDeadline(s.ctx, minTime(deadline, time.Now().Add(8*time.Second)))
	defer cancel()
	if err := s.LINE.Reply(replyCtx, j.event.ReplyToken, text); err != nil {
		s.Log.Warn("LINE reply failed", "event_id", eventID(j.event), "error", err.Error())
	}
}

func (s *Server) showLoading(ctx context.Context, e line.Event) {
	indicator, ok := s.LINE.(loadingIndicator)
	// Group events can contain userId too: only animate an explicit user chat.
	if !ok || e.Source.Type != "user" || e.Source.UserID == "" || e.Source.GroupID != "" || e.Source.RoomID != "" {
		return
	}
	// Keep the webhook ACK independent of LINE and cap this optional call so
	// an animation outage cannot use up the model/reply budget. Do not retry.
	loadingCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := indicator.ShowLoading(loadingCtx, e.Source.UserID, 60); err != nil {
		s.Log.Warn("line_loading_failed", "event_id", eventID(e), "error", err.Error())
	}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func (s *Server) query(w http.ResponseWriter, r *http.Request) {
	if s.Config.DemoToken == "" {
		respond(w, 404, map[string]string{"error": "not_found"})
		return
	}
	provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	a, b := sha256.Sum256([]byte(provided)), sha256.Sum256([]byte(s.Config.DemoToken))
	if subtle.ConstantTimeCompare(a[:], b[:]) != 1 {
		respond(w, 401, map[string]string{"error": "unauthorized"})
		return
	}
	var input struct {
		Text string `json:"text"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
	d.DisallowUnknownFields()
	if d.Decode(&input) != nil {
		respond(w, 400, map[string]string{"error": "invalid_json"})
		return
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		respond(w, 400, map[string]string{"error": "invalid_json"})
		return
	}
	if strings.TrimSpace(input.Text) == "" || len([]rune(input.Text)) > 5000 {
		respond(w, 400, map[string]string{"error": "text_required_max_5000_characters"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	respond(w, 200, s.answer(ctx, input.Text))
}
