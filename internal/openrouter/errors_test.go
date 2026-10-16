package openrouter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUpstreamFailuresAreClassifiedIncludingHTTP200Errors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		kind   ErrorKind
		code   int
	}{
		{"nvidia timeout", 504, `{"error":{"code":504,"message":"raw-secret","metadata":{"provider_name":"NVIDIA","error_type":"timeout"}}}`, Timeout, 504},
		{"provider rate limit", 429, `{"error":{"code":429,"metadata":{"provider_name":"Google AI Studio","error_type":"rate_limit_exceeded"}}}`, RateLimited, 429},
		{"embedded rate limit", 200, `{"error":{"code":"429","message":"raw-secret"}}`, RateLimited, 429},
		{"typed provider limit", 502, `{"error":{"code":502,"metadata":{"error_type":"rate_limit_exceeded"}}}`, RateLimited, 502},
		{"auth", 401, `{"error":{"code":401}}`, Authentication, 401},
		{"billing", 402, `{"error":{"code":402}}`, Billing, 402},
		{"budget", 403, `{"error":{"code":403,"metadata":{"limit_source":"openrouter_key_limit"}}}`, Billing, 403},
		{"forbidden", 403, `{}`, Forbidden, 403},
		{"bad request", 400, `{"error":{"code":400}}`, BadRequest, 400},
		{"provider unavailable", 503, `{"error":{"code":503}}`, Unavailable, 503},
		{"invalid json", 200, `not JSON raw-secret`, InvalidResponse, 0},
		{"no choices", 200, `{}`, InvalidResponse, 0},
		{"error html", 502, `<html>raw-secret</html>`, Unavailable, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Request-ID", "request-123")
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			c := New("test-key", "")
			c.URL = server.URL
			_, err := c.Complete(context.Background(), nil, nil, "auto")
			var api *APIError
			if !errors.As(err, &api) || api.Kind != tc.kind || api.HTTPStatus != tc.status || api.Code != tc.code || api.RequestID != "request-123" {
				t.Fatalf("got %+v, want %s code %d", err, tc.kind, tc.code)
			}
			if strings.Contains(err.Error(), "raw-secret") {
				t.Fatal("raw error leaked", err)
			}
			if tc.kind == RateLimited && !errors.Is(err, ErrRateLimited) {
				t.Fatal("lost rate-limit sentinel")
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCallDeadlineAllowsMoreThan20SecondsButRespectsParent(t *testing.T) {
	for _, parentBudget := range []time.Duration{time.Minute, 5 * time.Second} {
		c := New("test-key", "")
		c.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
			deadline, ok := r.Context().Deadline()
			left := time.Until(deadline)
			want := min(parentBudget, MaxCallTimeout)
			if !ok || left > want || left < want-time.Second {
				t.Errorf("deadline %s, want %s", left, want)
			}
			return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}]}`))}, nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), parentBudget)
		_, err := c.Complete(ctx, nil, nil, "none")
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestHTTPTimeoutKeepsCancellationCause(t *testing.T) {
	c := New("test-key", "")
	c.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := c.Complete(ctx, nil, nil, "auto")
	var api *APIError
	if !errors.As(err, &api) || api.Kind != Timeout || api.TimeoutSource != "client_deadline" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, retry := RetryDelay(err); retry {
		t.Fatal("client cancellation must not trigger a new upstream request")
	}
}

func TestRetryAfterAndRedaction(t *testing.T) {
	now := time.Date(2026, 10, 16, 6, 0, 0, 0, time.UTC)
	if retryAfter(now.Add(5*time.Second).Format(http.TimeFormat), now) != 5*time.Second {
		t.Fatal("HTTP-date Retry-After failed")
	}
	if retryAfter("-1", now) != 0 || retryAfter("invalid", now) != 0 || retryAfter("999999999", now) != 24*time.Hour {
		t.Fatal("invalid Retry-After bounds")
	}
	jsonBody := `{"code":429,"metadata":{"provider_name":"sk-or-v1-secret","model_slug":"Bearer private","error_type":"rate_limit_exceeded"}}`
	// Decode through a server so both the wire payload and safe error are covered.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		io.WriteString(w, `{"error":`+jsonBody+`}`)
	}))
	defer server.Close()
	c := New("test-key", "")
	c.URL = server.URL
	_, err := c.Complete(context.Background(), nil, nil, "auto")
	var api *APIError
	if !errors.As(err, &api) || api.Provider != "" || api.Model != "" {
		t.Fatal(err)
	}
	if _, retry := RetryDelay(err); retry {
		t.Fatal("429 without Retry-After must not be guessed")
	}
}
