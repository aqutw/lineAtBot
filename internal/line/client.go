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
	HTTP  *http.Client
	Token string
	URL   string
}

func New(token string) *Client {
	return &Client{HTTP: &http.Client{Timeout: 8 * time.Second}, Token: token, URL: "https://api.line.me/v2/bot/message/reply"}
}
func (c *Client) Reply(ctx context.Context, replyToken, text string) error {
	if c.Token == "" || replyToken == "" {
		return fmt.Errorf("LINE credentials unavailable")
	}
	body, err := json.Marshal(map[string]any{"replyToken": replyToken, "messages": []map[string]string{{"type": "text", "text": text}}})
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
