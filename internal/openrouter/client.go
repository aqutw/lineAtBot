package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

var ErrUnavailable = errors.New("model unavailable")
var ErrRateLimited = errors.New("model quota exhausted")

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
type Message struct {
	Role             string          `json:"role"`
	Content          string          `json:"content,omitempty"`
	ToolCalls        []ToolCall      `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	ReasoningDetails json.RawMessage `json:"reasoning_details,omitempty"`
}
type Tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}
type Completion struct {
	Message      Message
	Model        string
	FinishReason string
}
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
	return &Client{HTTP: &http.Client{Timeout: 20 * time.Second}, APIKey: key, Model: model, URL: "https://openrouter.ai/api/v1/chat/completions"}
}

// Space all completions, including tool follow-ups, within the free RPM budget.
func (c *Client) waitTurn(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delay := time.Until(c.lastRequest.Add(3100 * time.Millisecond))
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	c.lastRequest = time.Now()
	return nil
}
func (c *Client) Complete(ctx context.Context, messages []Message, tools []Tool, choice string) (Completion, error) {
	if c.APIKey == "" {
		return Completion{}, ErrUnavailable
	}
	if c.Model != "openrouter/free" && !strings.HasSuffix(c.Model, ":free") {
		return Completion{}, errors.New("only free models are enabled")
	}
	if err := c.waitTurn(ctx); err != nil {
		return Completion{}, err
	}
	body, err := json.Marshal(map[string]any{
		"model": c.Model, "stream": false, "max_tokens": 4096,
		"messages": messages, "tools": tools, "tool_choice": choice,
		"provider": map[string]any{"require_parameters": true},
	})
	if err != nil {
		return Completion{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return Completion{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Title", "lineAtBot")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return Completion{}, ErrUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests {
		return Completion{}, ErrRateLimited
	}
	if resp.StatusCode != http.StatusOK {
		return Completion{}, fmt.Errorf("%w (HTTP %d)", ErrUnavailable, resp.StatusCode)
	}
	var result struct {
		Error   *json.RawMessage `json:"error"`
		Model   string           `json:"model"`
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 512*1024)).Decode(&result); err != nil || result.Error != nil || len(result.Choices) == 0 {
		return Completion{}, ErrUnavailable
	}
	first := result.Choices[0]
	if len(first.Message.ToolCalls) == 0 && strings.TrimSpace(first.Message.Content) == "" {
		return Completion{}, ErrUnavailable
	}
	if first.FinishReason != "stop" && first.FinishReason != "tool_calls" && first.FinishReason != "length" {
		return Completion{}, ErrUnavailable
	}
	first.Message.Role = "assistant"
	return Completion{Message: first.Message, Model: result.Model, FinishReason: first.FinishReason}, nil
}
