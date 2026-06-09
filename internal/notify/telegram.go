package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Telegram sends notifications via the Telegram Bot API. Token + chat id come
// from configuration; the Bot token is a secret supplied at runtime.
type Telegram struct {
	Token  string
	ChatID string
	Client httpDoer
}

func (t Telegram) client() httpDoer {
	if t.Client != nil {
		return t.Client
	}
	return http.DefaultClient
}

// Notify posts the event message to the configured chat.
func (t Telegram) Notify(ctx context.Context, e Event) error {
	form := url.Values{"chat_id": {t.ChatID}, "text": {e.Message()}}
	endpoint := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.Token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.client().Do(req)
	if err != nil {
		return fmt.Errorf("notify: telegram: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("notify: telegram status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
