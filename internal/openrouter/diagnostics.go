package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// Attempt contains only allowlisted diagnostic fields, never raw messages,
// prompts, routing summaries, candidate endpoints or upstream payloads.
type Attempt struct {
	Model, Provider, ErrorType string
	Kind                       ErrorKind
	Code                       int
	TimeoutSource              string
}

func publicName(s string) string {
	s = safeID(s)
	if strings.Contains(strings.ToLower(s), "openrouter") || strings.Contains(s, "://") {
		return ""
	}
	return s
}

func (a Attempt) sanitized() Attempt {
	a.Model, a.Provider = publicName(a.Model), publicName(a.Provider)
	a.ErrorType = safeID(a.ErrorType)
	if a.Code < 400 || a.Code > 599 {
		a.Code = 0
	}
	return a
}

// FailureAttempts returns only reported failures. A selected/candidate endpoint
// or a model from a prior successful turn is not evidence of a failed attempt.
func FailureAttempts(err error) []Attempt {
	var api *APIError
	if !errors.As(err, &api) {
		a := Attempt{Kind: KindOf(err)}
		if errors.Is(err, context.DeadlineExceeded) {
			a.TimeoutSource = "client_deadline"
		}
		return []Attempt{a}
	}
	var out []Attempt
	for _, a := range api.Attempts {
		if len(out) == 12 {
			break
		}
		out = append(out, a.sanitized())
	}
	final := (Attempt{Model: api.Model, Provider: api.Provider, ErrorType: api.ErrorType,
		Kind: api.Kind, Code: api.Code, TimeoutSource: api.TimeoutSource}).sanitized()
	if len(out) == 0 {
		return []Attempt{final}
	}
	// Error-envelope metadata describes the last failed attempt when named.
	// Do not add an unnamed aggregate error as another upstream attempt.
	if final.Model != "" || final.Provider != "" {
		last := out[len(out)-1]
		if (final.Model == "" || final.Model == last.Model) && (final.Provider == "" || final.Provider == last.Provider) && (final.Code == 0 || final.Code == last.Code) {
			if final.ErrorType != "" {
				last.ErrorType, last.Kind = final.ErrorType, final.Kind
			}
			out[len(out)-1] = last
		} else if len(out) < 12 {
			out = append(out, final)
		}
	}
	return out
}

func routingAttempts(raw json.RawMessage) []Attempt {
	var routing struct {
		Attempts []json.RawMessage `json:"attempts"`
	}
	if json.Unmarshal(raw, &routing) != nil {
		return nil
	}
	var out []Attempt
	for _, rawAttempt := range routing.Attempts {
		if len(out) == 12 {
			break
		}
		var item struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
			Status   int    `json:"status"`
		}
		if json.Unmarshal(rawAttempt, &item) != nil || item.Status < 400 || item.Status > 599 {
			continue
		}
		classified := responseError(item.Status, http.Header{}, nil)
		out = append(out, (Attempt{Model: item.Model, Provider: item.Provider, Code: item.Status,
			Kind: classified.Kind, TimeoutSource: classified.TimeoutSource}).sanitized())
	}
	return out
}
