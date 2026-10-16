package web

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aqutw/lineAtBot/internal/line"
)

type loadingLINE struct {
	replyFunc
	loading func(context.Context, string, int) error
}

func (l loadingLINE) ShowLoading(ctx context.Context, userID string, seconds int) error {
	return l.loading(ctx, userID, seconds)
}

func TestLoadingBeforeAnswerDoesNotBlockWebhookOrRepeatOnRedelivery(t *testing.T) {
	s, model, replies := fixture(t)
	started, release := make(chan struct{}), make(chan struct{}, 1)
	defer close(release)
	var calls atomic.Int32
	s.LINE = loadingLINE{
		replyFunc: replies.Reply,
		loading: func(ctx context.Context, userID string, seconds int) error {
			calls.Add(1)
			if userID != "U-test-user" || seconds != 60 {
				t.Error("incorrect chat or duration")
			}
			if d, ok := ctx.Deadline(); !ok || time.Until(d) > time.Second {
				t.Error("missing bounded loading timeout")
			}
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
			}
			return nil
		},
	}
	body := []byte(`{"events":[{"type":"message","webhookEventId":"loading-test","replyToken":"reply-loading","source":{"type":"user","userId":"U-test-user"},"message":{"type":"text","text":"台積電收盤價"}}]}`)
	if w := post(s, "/line/webhook", body, "invalid", ""); w.Code != 403 || calls.Load() != 0 {
		t.Fatal("invalid signature triggered loading")
	}
	if w := post(s, "/line/webhook", body, signature(body), ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("no loading call")
	}
	model.mu.Lock()
	modelCalls := model.calls
	model.mu.Unlock()
	if modelCalls != 0 {
		t.Fatal("model ran before loading completed")
	}
	if w := post(s, "/line/webhook", body, signature(body), ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	// Release without closing twice; the deferred close handles test failures.
	release <- struct{}{}
	select {
	case got := <-replies.replies:
		if got.token != "reply-loading" || got.text != model.text || calls.Load() != 1 {
			t.Fatal(got, calls.Load())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no final reply")
	}
	select {
	case <-replies.replies:
		t.Fatal("duplicate final reply")
	default:
	}
}

func TestLoadingFailureAndTimeoutStillProduceFinalReply(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "HTTP failure"
		if timeout {
			name = "timeout"
		}
		t.Run(name, func(t *testing.T) {
			s, model, replies := fixture(t)
			s.LINE = loadingLINE{replyFunc: replies.Reply, loading: func(ctx context.Context, _ string, _ int) error {
				if timeout {
					<-ctx.Done()
					return ctx.Err()
				}
				return errors.New("LINE loading HTTP 429")
			}}
			var e line.Event
			e.ID, e.ReplyToken, e.Message.Text = "loading-failure", "final-token", "查價"
			e.Source.Type, e.Source.UserID = "user", "U-test-user"
			start := time.Now()
			s.process(job{event: e, received: start})
			if time.Since(start) > 2*time.Second {
				t.Fatal("loading consumed reply budget")
			}
			select {
			case got := <-replies.replies:
				if got.text != model.text || got.token != "final-token" {
					t.Fatal(got)
				}
			default:
				t.Fatal("animation failure suppressed answer")
			}
		})
	}
}

func TestLoadingOnlyForEligiblePrivateChats(t *testing.T) {
	for _, source := range []string{"group", "room", "missing-user", "missing-type", "mixed-source", "busy", "expired"} {
		t.Run(source, func(t *testing.T) {
			s, _, replies := fixture(t)
			s.LINE = loadingLINE{replyFunc: replies.Reply, loading: func(context.Context, string, int) error {
				t.Error("ineligible event triggered loading")
				return nil
			}}
			var e line.Event
			e.ID, e.ReplyToken, e.Message.Text = "ineligible", "token", "查價"
			e.Source.Type, e.Source.UserID = source, "U-test-user"
			received := time.Now()
			switch source {
			case "group":
				e.Source.GroupID = "C-test-group"
			case "room":
				e.Source.RoomID = "R-test-room"
			case "missing-user":
				e.Source.Type, e.Source.UserID = "user", ""
			case "missing-type":
				e.Source.Type = ""
			case "mixed-source":
				e.Source.Type, e.Source.GroupID = "user", "C-test-group"
			case "busy":
				e.Source.Type = "user"
				received = received.Add(-43 * time.Second)
			case "expired":
				e.Source.Type = "user"
				received = received.Add(-53 * time.Second)
			}
			s.process(job{event: e, received: received})
		})
	}
}
