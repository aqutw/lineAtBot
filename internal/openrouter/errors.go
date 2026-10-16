package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type ErrorKind string

const (
	RateLimited     ErrorKind = "rate_limited"
	Timeout         ErrorKind = "timeout"
	Cancelled       ErrorKind = "cancelled"
	Authentication  ErrorKind = "authentication"
	Billing         ErrorKind = "billing_or_budget"
	Forbidden       ErrorKind = "forbidden"
	BadRequest      ErrorKind = "bad_request"
	Unavailable     ErrorKind = "upstream_unavailable"
	InvalidResponse ErrorKind = "invalid_response"
	Configuration   ErrorKind = "configuration"
)

// APIError intentionally excludes upstream message/raw payloads. They can
// contain credentials or user data and must not enter logs or LINE replies.
type APIError struct {
	Kind          ErrorKind
	HTTPStatus    int
	Code          int
	ErrorType     string
	RequestID     string
	Provider      string
	Model         string
	RetryAfter    time.Duration
	TimeoutSource string
	Attempts      []Attempt
	Cause         error
}

func (e *APIError) Error() string {
	return fmt.Sprintf("openrouter %s (http=%d code=%d)", e.Kind, e.HTTPStatus, e.Code)
}
func (e *APIError) Unwrap() error { return e.Cause }

type wireError struct {
	Code     json.RawMessage `json:"code"`
	Metadata struct {
		ErrorType   string `json:"error_type"`
		Provider    string `json:"provider_name"`
		Model       string `json:"model_slug"`
		LimitSource string `json:"limit_source"`
	} `json:"metadata"`
}

var safeIdentifier = regexp.MustCompile(`^[A-Za-z0-9._:/ -]{1,128}$`)

func safeID(s string) string {
	// Never echo an accidentally supplied credential, even in metadata fields.
	if !safeIdentifier.MatchString(s) || strings.Contains(strings.ToLower(s), "sk-") || strings.Contains(strings.ToLower(s), "bearer") {
		return ""
	}
	return s
}

func responseError(status int, header http.Header, wire *wireError) *APIError {
	e := &APIError{HTTPStatus: status, Code: status, RequestID: safeID(header.Get("X-Request-ID")), Cause: ErrUnavailable}
	if wire != nil {
		var number int
		if json.Unmarshal(wire.Code, &number) != nil {
			var text string
			if json.Unmarshal(wire.Code, &text) == nil {
				number, _ = strconv.Atoi(text)
			}
		}
		if number >= 400 && number <= 599 {
			e.Code = number
		}
		e.ErrorType, e.Provider, e.Model = safeID(wire.Metadata.ErrorType), safeID(wire.Metadata.Provider), safeID(wire.Metadata.Model)
	}
	code := e.Code
	// A definite HTTP auth/permission/billing error outranks conflicting body data.
	if status == 400 || status == 401 || status == 402 || status == 403 {
		code = status
	}
	switch code {
	case 401:
		e.Kind = Authentication
	case 402:
		e.Kind = Billing
	case 403:
		e.Kind = Forbidden
	case 408, 504:
		e.Kind, e.TimeoutSource = Timeout, "upstream"
	case 499:
		e.Kind = Cancelled
	case 429:
		e.Kind, e.Cause = RateLimited, ErrRateLimited
	case 400, 404, 413, 422:
		e.Kind = BadRequest
	default:
		e.Kind = Unavailable
		if e.ErrorType == "rate_limit_exceeded" {
			e.Kind, e.Cause = RateLimited, ErrRateLimited
		}
		if e.ErrorType == "timeout" {
			e.Kind, e.TimeoutSource = Timeout, "upstream"
		}
	}
	if code == 403 && wire != nil && (wire.Metadata.LimitSource == "openrouter_key_limit" || (strings.HasPrefix(wire.Metadata.LimitSource, "openrouter_") && strings.Contains(wire.Metadata.LimitSource, "budget"))) {
		e.Kind = Billing
	}
	e.RetryAfter = retryAfter(header.Get("Retry-After"), time.Now())
	return e
}

func retryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > 86400 {
			seconds = 86400
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil && at.After(now) {
		return min(at.Sub(now), 24*time.Hour)
	}
	return 0
}

func KindOf(err error) ErrorKind {
	if err == nil {
		return ""
	}
	var api *APIError
	if errors.As(err, &api) {
		return api.Kind
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Timeout
	}
	if errors.Is(err, context.Canceled) {
		return Cancelled
	}
	if errors.Is(err, ErrRateLimited) {
		return RateLimited
	}
	return Unavailable
}

// RetryDelay permits only one retry at the caller. Missing Retry-After on a
// 429 is deliberately not guessed: OpenRouter has already attempted failover.
func RetryDelay(err error) (time.Duration, bool) {
	var api *APIError
	if !errors.As(err, &api) {
		return 0, false
	}
	if api.Kind == RateLimited {
		return api.RetryAfter, api.RetryAfter > 0
	}
	if (api.Kind == Unavailable || api.Kind == Timeout) && (api.HTTPStatus == 408 || api.HTTPStatus == 502 || api.HTTPStatus == 503 || api.HTTPStatus == 504 || api.Code == 502 || api.Code == 503 || api.Code == 504) {
		return max(time.Second, api.RetryAfter), true
	}
	return 0, false
}
