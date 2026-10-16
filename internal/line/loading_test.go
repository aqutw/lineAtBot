package line

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestShowLoadingContractAndSanitizedErrors(t *testing.T) {
	for _, status := range []int{202, 200, 400, 429, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v2/bot/chat/loading/start" || r.Header.Get("Authorization") != "Bearer test-token" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("unexpected request method, path or headers")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 2 || body["chatId"] != "U-test-user" || body["loadingSeconds"] != float64(60) {
					t.Error("incorrect loading payload", body, err)
				}
				w.WriteHeader(status)
				fmt.Fprint(w, `{"message":"private upstream details"}`)
			}))
			defer server.Close()
			c := New("test-token")
			c.LoadingURL = server.URL + "/v2/bot/chat/loading/start"
			err := c.ShowLoading(context.Background(), "U-test-user", 60)
			if status == http.StatusAccepted {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != fmt.Sprintf("LINE loading HTTP %d", status) {
				t.Fatal("missing sanitized HTTP error", err)
			}
		})
	}
}

func TestShowLoadingRejectsInvalidArguments(t *testing.T) {
	c := New("test-token")
	c.LoadingURL = ":invalid-url"
	for _, seconds := range []int{-5, 0, 1, 6, 61, 65} {
		if err := c.ShowLoading(context.Background(), "U-test-user", seconds); err == nil || !strings.Contains(err.Error(), "duration invalid") {
			t.Fatal(seconds, err)
		}
	}
	if err := c.ShowLoading(context.Background(), "", 60); err == nil {
		t.Fatal("accepted missing user ID")
	}
	c.Token = ""
	if err := c.ShowLoading(context.Background(), "U-test-user", 60); err == nil {
		t.Fatal("accepted missing token")
	}
}

func TestShowLoadingHonorsContextDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		<-r.Context().Done()
	}))
	defer server.Close()
	c := New("test-token")
	c.LoadingURL = server.URL
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := c.ShowLoading(ctx, "U-test-user", 60); err == nil || err.Error() != "LINE loading transport failed" {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("ignored optional-call timeout")
	}
}
