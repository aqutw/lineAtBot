package line

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

func VerifySignature(body []byte, signature, secret string) bool {
	if secret == "" || signature == "" {
		return false
	}
	provided, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return false
	}
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(body)
	return hmac.Equal(provided, h.Sum(nil))
}

type Event struct {
	Type       string `json:"type"`
	ReplyToken string `json:"replyToken"`
	ID         string `json:"webhookEventId"`
	Source     struct {
		Type    string `json:"type"`
		UserID  string `json:"userId"`
		GroupID string `json:"groupId"`
		RoomID  string `json:"roomId"`
	} `json:"source"`
	Message struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"message"`
}
type Client struct {
	HTTP       *http.Client
	Token      string
	URL        string
	LoadingURL string
}

func New(token string) *Client {
	return &Client{HTTP: &http.Client{Timeout: 8 * time.Second}, Token: token,
		URL: "https://api.line.me/v2/bot/message/reply", LoadingURL: "https://api.line.me/v2/bot/chat/loading/start"}
}

// ShowLoading uses a user ID, never a reply token. LINE clears the animation
// when the final message arrives; this endpoint does not send a text message.
func (c *Client) ShowLoading(ctx context.Context, userID string, seconds int) error {
	if c.Token == "" || userID == "" {
		return fmt.Errorf("LINE loading credentials unavailable")
	}
	if seconds < 5 || seconds > 60 || seconds%5 != 0 {
		return fmt.Errorf("LINE loading duration invalid")
	}
	body, err := json.Marshal(map[string]any{"chatId": userID, "loadingSeconds": seconds})
	if err != nil {
		return fmt.Errorf("LINE loading encoding failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.LoadingURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("LINE loading request invalid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("LINE loading transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("LINE loading HTTP %d", resp.StatusCode)
	}
	return nil
}
func (c *Client) Reply(ctx context.Context, replyToken, text string) error {
	if c.Token == "" || replyToken == "" {
		return fmt.Errorf("LINE credentials unavailable")
	}
	messages, err := splitMessages(text)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]any{"replyToken": replyToken, "messages": messages})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("LINE reply transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("LINE reply HTTP %d", resp.StatusCode)
	}
	return nil
}

// LINE permits up to five text messages of 5000 UTF-16 units per reply.
// Preserve the model's text exactly; only split it for the transport limit.
func splitMessages(text string) ([]map[string]string, error) {
	if text == "" {
		return nil, fmt.Errorf("LINE reply text is empty")
	}
	var out []map[string]string
	start, units := 0, 0
	for offset, r := range text {
		width := 1
		if r > 0xffff {
			width = 2
		}
		if units+width > 5000 {
			out = append(out, map[string]string{"type": "text", "text": text[start:offset]})
			start, units = offset, 0
		}
		units += width
	}
	out = append(out, map[string]string{"type": "text", "text": text[start:]})
	if len(out) > 5 {
		return nil, fmt.Errorf("LINE reply exceeds five messages")
	}
	return out, nil
}
