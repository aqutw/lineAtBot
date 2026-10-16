package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

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
	RequestID    string
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
		return Completion{}, &APIError{Kind: Configuration, Cause: ErrUnavailable}
	}
	if c.Model != "openrouter/free" && !strings.HasSuffix(c.Model, ":free") {
		return Completion{}, &APIError{Kind: Configuration, Cause: errors.New("only free models are enabled")}
	}
	if err := c.waitTurn(ctx); err != nil {
		return Completion{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, MaxCallTimeout)
	defer cancel()
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
		return Completion{}, transportError(ctx, err)
	}
	defer resp.Body.Close()
	bodyBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, 512*1024+1))
	if readErr != nil {
		failure := transportError(ctx, readErr)
		failure.HTTPStatus = resp.StatusCode
		failure.RequestID = safeID(resp.Header.Get("X-Request-ID"))
		return Completion{}, failure
	}
	var result struct {
		Error   *wireError `json:"error"`
		Model   string     `json:"model"`
		ID      string     `json:"id"`
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
	}
	decodeErr := json.Unmarshal(bodyBytes, &result)
	if resp.StatusCode != http.StatusOK || (decodeErr == nil && result.Error != nil) {
		failure := responseError(resp.StatusCode, resp.Header, result.Error)
		if failure.RequestID == "" {
			failure.RequestID = safeID(result.ID)
		}
		return Completion{}, failure
	}
	if decodeErr != nil || len(bodyBytes) > 512*1024 || len(result.Choices) == 0 {
		return Completion{}, &APIError{Kind: InvalidResponse, HTTPStatus: resp.StatusCode, RequestID: safeID(resp.Header.Get("X-Request-ID")), Cause: ErrUnavailable}
	}
	first := result.Choices[0]
	if first.FinishReason != "stop" && first.FinishReason != "tool_calls" && first.FinishReason != "length" {
		return Completion{}, &APIError{Kind: InvalidResponse, HTTPStatus: resp.StatusCode, RequestID: safeID(result.ID), Model: safeID(result.Model), Cause: ErrUnavailable}
	}
	first.Message.Role = "assistant"
	return Completion{Message: first.Message, Model: safeID(result.Model), FinishReason: first.FinishReason, RequestID: safeID(result.ID)}, nil
}

func transportError(ctx context.Context, err error) *APIError {
	if ctx.Err() != nil {
		return &APIError{Kind: KindOf(ctx.Err()), TimeoutSource: "client_deadline", Cause: ctx.Err()}
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return &APIError{Kind: Timeout, TimeoutSource: "transport", Cause: context.DeadlineExceeded}
	}
	return &APIError{Kind: Unavailable, Cause: ErrUnavailable}
}
