package audit

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/secrets"
)

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decode %q: %v", b, err)
	}
	return m
}

func TestRecord_AttributionAndAction(t *testing.T) {
	var buf bytes.Buffer
	a := New(&buf)
	a.Record(Entry{
		Action:    ActionApply,
		RolloutID: "ro-1",
		TargetRef: "petmed/prod/api",
		Phase:     "deploying",
		Actor:     rollout.Identity{Kind: "agent", Name: "nomi"},
	})
	m := decode(t, buf.Bytes())
	if m["action"] != "apply" || m["actor"] != "nomi" || m["actor_kind"] != "agent" {
		t.Errorf("attribution missing: %v", m)
	}
	if m["target_ref"] != "petmed/prod/api" || m["rollout_id"] != "ro-1" {
		t.Errorf("fields missing: %v", m)
	}
}

func TestRecord_RedactsSecretField(t *testing.T) {
	var buf bytes.Buffer
	a := New(&buf)
	a.Record(Entry{
		Action: ActionApply,
		Actor:  rollout.Identity{Kind: "human", Name: "felix"},
		Fields: map[string]any{"db_password": secrets.NewSecret("hunter2")},
	})
	out := buf.String()
	if strings.Contains(out, "hunter2") {
		t.Errorf("secret leaked into audit: %s", out)
	}
	if !strings.Contains(out, "***") {
		t.Errorf("expected redaction marker: %s", out)
	}
}

func TestRedactor_ScrubsValueFromText(t *testing.T) {
	var buf bytes.Buffer
	a := New(&buf)
	a.Redactor().Register(secrets.NewSecret("topsecret"))
	a.Record(Entry{
		Action: ActionDrift,
		Actor:  rollout.Identity{Kind: "ci", Name: "pipeline-7"},
		Detail: "connected with token topsecret to host",
	})
	out := buf.String()
	if strings.Contains(out, "topsecret") {
		t.Errorf("registered secret not scrubbed from detail: %s", out)
	}
}
