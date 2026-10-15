package line

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestReplyPreservesLongUnicodeModelText(t *testing.T) {
	text := strings.Repeat("股價😀", 3000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct{ Type, Text string }
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		var combined strings.Builder
		if len(payload.Messages) != 3 {
			t.Errorf("got %d chunks", len(payload.Messages))
		}
		for _, message := range payload.Messages {
			if message.Type != "text" || len(utf16.Encode([]rune(message.Text))) > 5000 {
				t.Error("invalid LINE chunk")
			}
			combined.WriteString(message.Text)
		}
		if combined.String() != text {
			t.Error("model response was changed")
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	c := New("test-token")
	c.URL = server.URL
	if err := c.Reply(context.Background(), "test-reply-token", text); err != nil {
		t.Fatal(err)
	}
}
