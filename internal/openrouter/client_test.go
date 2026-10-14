package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseUsesSchemaAndRejectsInvalidOutput(t *testing.T) {
	for _, test := range []struct {
		name, content, reason string
		status                int
		wantErr               bool
	}{
		{"valid", `{"intent":"latest_close","stock":"2330","date":"","start_date":"","end_date":"","limit":1}`, "stop", 200, false},
		{"SQL field", `{"intent":"latest_close","stock":"2330","sql":"DROP TABLE prices","limit":1}`, "stop", 200, true},
		{"invalid intent", `{"intent":"delete","stock":"2330","limit":1}`, "stop", 200, true},
		{"truncated", `{"intent":"latest_close","stock":"2330","limit":1}`, "length", 200, true},
		{"second JSON", `{"intent":"latest_close","stock":"2330","limit":1} {}`, "stop", 200, true},
		{"quota", "", "", 429, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test-key" {
					t.Error("incorrect auth format")
				}
				var input map[string]any
				if json.NewDecoder(r.Body).Decode(&input) != nil {
					t.Error("bad request")
				}
				if input["model"] != "openrouter/free" || input["response_format"].(map[string]any)["type"] != "json_schema" {
					t.Error("missing free model/schema")
				}
				if input["provider"].(map[string]any)["require_parameters"] != true {
					t.Error("schema support not required")
				}
				w.WriteHeader(test.status)
				json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"finish_reason": test.reason, "message": map[string]string{"content": test.content}}}})
			}))
			defer upstream.Close()
			client := New("Bearer test-key", "openrouter/free")
			client.URL = upstream.URL
			_, err := client.Parse(context.Background(), "台積電最近收盤多少", time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC))
			if (err != nil) != test.wantErr {
				t.Fatalf("error=%v", err)
			}
			if test.status == 429 && !errors.Is(err, ErrRateLimited) {
				t.Fatal(err)
			}
		})
	}
}
