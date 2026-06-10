package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/smtp"
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

func TestEmail_Notify(t *testing.T) {
	var gotAddr, gotFrom string
	var gotTo []string
	var gotMsg []byte
	em := Email{
		Addr: "smtp.example.com:587",
		From: "rollops@example.com",
		To:   []string{"ops@example.com", "oncall@example.com"},
		Send: func(addr string, _ smtp.Auth, from string, to []string, msg []byte) error {
			gotAddr, gotFrom, gotTo, gotMsg = addr, from, to, msg
			return nil
		},
	}
	e := Event{Kind: Failed, TargetRef: "prod/web", RolloutID: "ro-9", Detail: "boom"}
	if err := em.Notify(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if gotAddr != "smtp.example.com:587" || gotFrom != "rollops@example.com" {
		t.Errorf("addr=%q from=%q", gotAddr, gotFrom)
	}
	if len(gotTo) != 2 || gotTo[0] != "ops@example.com" {
		t.Errorf("to = %v", gotTo)
	}
	msg := string(gotMsg)
	if !strings.Contains(msg, "Subject: Rollops: prod/web failed") {
		t.Errorf("subject missing: %q", msg)
	}
	if !strings.Contains(msg, "To: ops@example.com, oncall@example.com") {
		t.Errorf("to header missing: %q", msg)
	}
	if !strings.Contains(msg, "boom") || !strings.Contains(msg, "ro-9") {
		t.Errorf("body missing detail: %q", msg)
	}
}

func TestEmail_SendError(t *testing.T) {
	em := Email{
		Addr: "smtp:25", From: "a@b", To: []string{"c@d"},
		Send: func(string, smtp.Auth, string, []string, []byte) error {
			return errors.New("connection refused")
		},
	}
	if err := em.Notify(context.Background(), Event{Kind: Failed}); err == nil {
		t.Fatal("expected error when send fails")
	}
}

func TestBriefkasten_Notify(t *testing.T) {
	var gotName string
	var gotArgs map[string]any
	bk := Briefkasten{
		URL: "http://127.0.0.1:8090",
		To:  []string{"ops@example.com"},
		Call: func(_ context.Context, name string, args any) (bool, string, error) {
			gotName = name
			gotArgs = args.(map[string]any)
			return false, `{"id":"m-1","state":"queued"}`, nil
		},
	}
	e := Event{Kind: RolledBack, TargetRef: "prod/web", RolloutID: "ro-3", Detail: "health gate"}
	if err := bk.Notify(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	if gotName != "email.send" {
		t.Errorf("tool = %q", gotName)
	}
	to := gotArgs["to"].([]string)
	if len(to) != 1 || to[0] != "ops@example.com" {
		t.Errorf("to = %v", to)
	}
	if gotArgs["subject"] != "Rollops: prod/web rolled back" {
		t.Errorf("subject = %v", gotArgs["subject"])
	}
	if body := gotArgs["body"].(string); !strings.Contains(body, "ro-3") || !strings.Contains(body, "health gate") {
		t.Errorf("body = %q", body)
	}
}

func TestBriefkasten_ToolError(t *testing.T) {
	bk := Briefkasten{
		URL: "http://x", To: []string{"a@b"},
		Call: func(context.Context, string, any) (bool, string, error) {
			return true, "outbox not configured", nil
		},
	}
	if err := bk.Notify(context.Background(), Event{Kind: Failed}); err == nil {
		t.Fatal("expected error on isError result")
	}
}

func TestBriefkasten_CallError(t *testing.T) {
	bk := Briefkasten{
		URL: "http://x", To: []string{"a@b"},
		Call: func(context.Context, string, any) (bool, string, error) {
			return false, "", errors.New("connect refused")
		},
	}
	if err := bk.Notify(context.Background(), Event{Kind: Failed}); err == nil {
		t.Fatal("expected error on transport failure")
	}
}

func TestFromEnv_Briefkasten(t *testing.T) {
	n, names := FromEnv(func(k string) string {
		return map[string]string{
			"ROLLOPS_BRIEFKASTEN_URL": "http://127.0.0.1:8090",
			"ROLLOPS_BRIEFKASTEN_TO":  "ops@example.com",
		}[k]
	})
	if n == nil || len(names) != 1 || names[0] != "briefkasten" {
		t.Fatalf("names = %v", names)
	}
	bk := n.(Multi)[0].(Briefkasten)
	if len(bk.To) != 1 || bk.To[0] != "ops@example.com" {
		t.Errorf("to = %v", bk.To)
	}
}

func TestEvent_Subject(t *testing.T) {
	s := Event{Kind: ApprovalNeeded, TargetRef: "pay/prod/api"}.Subject()
	if s != "Rollops: pay/prod/api needs approval" {
		t.Errorf("subject = %q", s)
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

func TestFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}

	t.Run("nothing configured", func(t *testing.T) {
		n, names := FromEnv(env(nil))
		if n != nil || names != nil {
			t.Errorf("got %v %v, want nil nil", n, names)
		}
	})

	t.Run("email only", func(t *testing.T) {
		n, names := FromEnv(env(map[string]string{
			"ROLLOPS_SMTP_ADDR": "smtp.example.com:587",
			"ROLLOPS_SMTP_FROM": "rollops@example.com",
			"ROLLOPS_SMTP_TO":   "ops@example.com, oncall@example.com",
		}))
		if n == nil || len(names) != 1 || names[0] != "email" {
			t.Fatalf("names = %v", names)
		}
		em := n.(Multi)[0].(Email)
		if len(em.To) != 2 || em.To[1] != "oncall@example.com" {
			t.Errorf("to = %v", em.To)
		}
	})

	t.Run("email auth from user/pass", func(t *testing.T) {
		n, _ := FromEnv(env(map[string]string{
			"ROLLOPS_SMTP_ADDR": "smtp.example.com:587",
			"ROLLOPS_SMTP_FROM": "a@b", "ROLLOPS_SMTP_TO": "c@d",
			"ROLLOPS_SMTP_USER": "u", "ROLLOPS_SMTP_PASS": "p",
		}))
		if n.(Multi)[0].(Email).Auth == nil {
			t.Error("auth should be set when user/pass present")
		}
	})

	t.Run("webhook only", func(t *testing.T) {
		n, names := FromEnv(env(map[string]string{"ROLLOPS_WEBHOOK_URL": "https://x"}))
		if n == nil || len(names) != 1 || names[0] != "webhook" {
			t.Errorf("names = %v", names)
		}
	})

	t.Run("both", func(t *testing.T) {
		n, names := FromEnv(env(map[string]string{
			"ROLLOPS_SMTP_ADDR": "smtp:587", "ROLLOPS_SMTP_FROM": "a@b", "ROLLOPS_SMTP_TO": "c@d",
			"ROLLOPS_WEBHOOK_URL": "https://x", "ROLLOPS_WEBHOOK_SECRET": "s",
		}))
		m, ok := n.(Multi)
		if !ok || len(m) != 2 {
			t.Fatalf("want Multi of 2, got %T %v", n, n)
		}
		if len(names) != 2 || names[0] != "email" || names[1] != "webhook" {
			t.Errorf("names = %v", names)
		}
	})
}

func TestEvent_Message_Test(t *testing.T) {
	m := Event{Kind: Test, TargetRef: "doctor"}.Message()
	if !strings.Contains(m, "test") {
		t.Errorf("message = %q", m)
	}
}
