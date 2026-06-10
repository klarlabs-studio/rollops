package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type rt struct {
	req  *http.Request
	body string
	code int
}

func (r *rt) Do(req *http.Request) (*http.Response, error) {
	b, _ := io.ReadAll(req.Body)
	r.req = req
	r.body = string(b)
	code := r.code
	if code == 0 {
		code = 200
	}
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
}

func TestEvent_Message(t *testing.T) {
	m := Event{Kind: ApprovalNeeded, TargetRef: "pay/prod/api", RolloutID: "ro-1"}.Message()
	if !strings.Contains(m, "pay/prod/api") || !strings.Contains(m, "needs approval") || !strings.Contains(m, "ro-1") {
		t.Errorf("message = %q", m)
	}
}

func TestTelegram_Notify(t *testing.T) {
	c := &rt{}
	tg := Telegram{Token: "TOK", ChatID: "123", Client: c}
	if err := tg.Notify(context.Background(), Event{Kind: Promoted, TargetRef: "a/b"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.req.URL.String(), "/botTOK/sendMessage") {
		t.Errorf("url = %s", c.req.URL)
	}
	if !strings.Contains(c.body, "chat_id=123") || !strings.Contains(c.body, "promoted") {
		t.Errorf("body = %s", c.body)
	}
}

func TestTelegram_NonOKError(t *testing.T) {
	tg := Telegram{Token: "T", ChatID: "1", Client: &rt{code: 403}}
	if err := tg.Notify(context.Background(), Event{Kind: Failed}); err == nil {
		t.Fatal("expected error on non-200")
	}
}

func TestWebhook_SignsAndPosts(t *testing.T) {
	c := &rt{}
	w := Webhook{URL: "https://hooks.example/x", Secret: "shh", Client: c}
	e := Event{Kind: Failed, TargetRef: "a/b", RolloutID: "ro-9", Detail: "boom"}
	if err := w.Notify(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	var got Event
	if err := json.Unmarshal([]byte(c.body), &got); err != nil {
		t.Fatalf("body not json: %v", err)
	}
	if got != e {
		t.Errorf("payload = %+v, want %+v", got, e)
	}
	mac := hmac.New(sha256.New, []byte("shh"))
	mac.Write([]byte(c.body))
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if c.req.Header.Get("X-Rollops-Signature") != want {
		t.Errorf("signature = %q, want %q", c.req.Header.Get("X-Rollops-Signature"), want)
	}
}

func TestMulti_FansOut(t *testing.T) {
	a, b := &rt{}, &rt{}
	m := Multi{Webhook{URL: "https://x/1", Client: a}, Webhook{URL: "https://x/2", Client: b}}
	if err := m.Notify(context.Background(), Event{Kind: Promoted, TargetRef: "t"}); err != nil {
		t.Fatal(err)
	}
	if a.req == nil || b.req == nil {
		t.Error("both notifiers should receive the event")
	}
}

func TestNoop(t *testing.T) {
	if err := (Noop{}).Notify(context.Background(), Event{}); err != nil {
		t.Fatal(err)
	}
}
