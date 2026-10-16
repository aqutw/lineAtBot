package openrouter

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

var ErrUnavailable = errors.New("model unavailable")
var ErrRateLimited = errors.New("model rate limited")

const MaxCallTimeout = 35 * time.Second

type Client struct {
	HTTP        *http.Client
	APIKey      string
	Model       string
	URL         string
	mu          sync.Mutex
	lastRequest time.Time
}

func New(key, model string) *Client {
	if model == "" {
		model = "openrouter/free"
	}
	key = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(key), "Bearer "))
	// Request contexts own the budget, including response-body reads.
	return &Client{HTTP: &http.Client{}, APIKey: key, Model: model, URL: "https://openrouter.ai/api/v1/chat/completions"}
}
