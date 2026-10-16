package openrouter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRoutingFailureAttemptsUseActualAttemptsNotCandidates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-OpenRouter-Metadata") != "enabled" {
			t.Error("missing routing metadata opt-in")
		}
		w.WriteHeader(429)
		io.WriteString(w, `{"error":{"code":429,"message":"private prompt and credential","metadata":{"provider_name":"Google AI Studio","model_slug":"google/model-b:free","error_type":"rate_limit_exceeded","raw":"secret body"}},"openrouter_metadata":{"requested":"openrouter/free","endpoints":{"available":[{"provider":"Never attempted","model":"untried/model","selected":false}]},"attempts":[{"provider":"NVIDIA","model":"vendor/model-a:free","status":504},{"provider":"Google AI Studio","model":"google/model-b:free","status":429}]}}`)
	}))
	defer server.Close()
	c := New("test-key", "openrouter/free")
	c.URL = server.URL
	_, err := c.Complete(context.Background(), nil, nil, "auto")
	a := FailureAttempts(err)
	if len(a) != 2 || a[0].Provider != "NVIDIA" || a[0].Model != "vendor/model-a:free" || a[0].Kind != Timeout || a[1].Provider != "Google AI Studio" || a[1].Kind != RateLimited {
		t.Fatalf("unexpected failures: %+v", a)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
		t.Fatal("raw error leaked")
	}
}

func TestEmbeddedChoiceErrorCarriesSafeActualModel(t *testing.T) {
	c := New("test-key", "")
	c.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"model":"vendor/model-c:free","provider":"NVIDIA","choices":[{"finish_reason":"error","error":{"code":504,"metadata":{"error_type":"timeout"}}}]}`))}, nil
	})
	_, err := c.Complete(context.Background(), nil, nil, "auto")
	a := FailureAttempts(err)
	if len(a) != 1 || a[0].Model != "vendor/model-c:free" || a[0].Provider != "NVIDIA" || a[0].Code != 504 {
		t.Fatalf("lost embedded error: %+v %v", a, err)
	}
}

func TestSuccessfulFallbackRetainsOnlyFailedAttempts(t *testing.T) {
	c := New("test-key", "")
	c.HTTP.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"model":"good-model","choices":[{"finish_reason":"stop","message":{"content":"正常答案"}}],"openrouter_metadata":{"attempts":[{"provider":"NVIDIA","model":"failed-model","status":504},{"provider":"good-provider","model":"good-model","status":200}]}}`))}, nil
	})
	result, err := c.Complete(context.Background(), nil, nil, "auto")
	if err != nil || result.Message.Content != "正常答案" || len(result.FailedAttempts) != 1 || result.FailedAttempts[0].Model != "failed-model" {
		t.Fatal(result, err)
	}
}

func TestRoutingMetadataIsOptionalAndCannotInventFailures(t *testing.T) {
	for _, raw := range []string{
		`null`, `{"attempts":"invalid"}`, `{"attempts":[{"provider":"candidate","model":"not-failed","status":200},{"provider":"candidate","status":0},{"status":"504"}]}`,
	} {
		if got := routingAttempts([]byte(raw)); len(got) != 0 {
			t.Fatal(got)
		}
	}
	a := FailureAttempts(&APIError{Kind: Timeout, Code: 504, Model: "openrouter/free", Provider: "Bearer secret", Attempts: []Attempt{{Kind: Timeout, Code: 504, Model: "sk-or-v1-secret", Provider: "https://private.invalid"}}})
	if len(a) != 1 || a[0].Model != "" || a[0].Provider != "" {
		t.Fatal(a)
	}
	if got := FailureAttempts(context.DeadlineExceeded); len(got) != 1 || got[0].Kind != Timeout || got[0].Model != "" {
		t.Fatal(got)
	}
	if got := FailureAttempts(errors.New("raw-secret")); got[0].Kind != Unavailable {
		t.Fatal(got)
	}
}
