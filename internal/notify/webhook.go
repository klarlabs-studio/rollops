package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
)

// Webhook POSTs the event as JSON to a URL. When Secret is set it adds an
// HMAC-SHA256 signature header so the receiver can verify authenticity.
type Webhook struct {
	URL    string
	Secret string // optional HMAC signing secret
	Client httpDoer
}

func (h Webhook) client() httpDoer {
	if h.Client != nil {
		return h.Client
	}
	return http.DefaultClient
}

// Notify posts the JSON-encoded event.
func (h Webhook) Notify(ctx context.Context, e Event) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.Secret != "" {
		mac := hmac.New(sha256.New, []byte(h.Secret))
		mac.Write(body)
		req.Header.Set("X-Rollops-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := h.client().Do(req)
	if err != nil {
		return fmt.Errorf("notify: webhook: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("notify: webhook status %d", resp.StatusCode)
	}
	return nil
}
