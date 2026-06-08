package config

import (
	"os"
	"strings"
	"testing"
)

const validYAML = `
apiVersion: rolloffs.klarlabs.de/v1
kind: RolloutConfig
metadata:
  name: pet-medical-api
  labels:
    team: platform
spec:
  target:
    kind: ssh
    ref: pet-medical/prod/api
    criticality: high
    spec:
      host: api.petmedical.internal
      path: /srv/api
  environments:
    - name: staging
      promote: true
    - name: prod
      promote: true
  strategy:
    type: canary
    steps:
      - weight: 10
        pause: 5m
      - weight: 50
        pause: 5m
  risk:
    threshold: 0.7
    sensitive: 'changeType == "schema"'
  rollback:
    auto: true
    healthCheck:
      http: https://api.petmedical.internal/healthz
      timeout: 10s
    smokeTest:
      command: ["./smoke.sh"]
      expectExit: 0
  dependsOn:
    - pet-medical/prod/db
  schedule: "2026-06-09T02:00:00Z"
`

func TestParse_Valid(t *testing.T) {
	c, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if c.APIVersion != SchemaVersion {
		t.Errorf("APIVersion = %q, want %q", c.APIVersion, SchemaVersion)
	}
	if c.Kind != Kind {
		t.Errorf("Kind = %q, want %q", c.Kind, Kind)
	}
	if c.Metadata.Name != "pet-medical-api" {
		t.Errorf("metadata.name = %q", c.Metadata.Name)
	}
	if c.Spec.Target.Kind != "ssh" || c.Spec.Target.Criticality != "high" {
		t.Errorf("target = %+v", c.Spec.Target)
	}
	if c.Spec.Target.Ref != "pet-medical/prod/api" {
		t.Errorf("target.ref = %q", c.Spec.Target.Ref)
	}
	if c.Spec.Strategy.Type != "canary" || len(c.Spec.Strategy.Steps) != 2 {
		t.Errorf("strategy = %+v", c.Spec.Strategy)
	}
	if c.Spec.Strategy.Steps[0].Weight != 10 || c.Spec.Strategy.Steps[0].Pause != "5m" {
		t.Errorf("step0 = %+v", c.Spec.Strategy.Steps[0])
	}
	if c.Spec.Risk.Threshold != 0.7 || c.Spec.Risk.Sensitive == "" {
		t.Errorf("risk = %+v", c.Spec.Risk)
	}
	if c.Spec.Rollback.HealthCheck == nil || c.Spec.Rollback.SmokeTest == nil {
		t.Fatalf("rollback checks not parsed: %+v", c.Spec.Rollback)
	}
	if len(c.Spec.DependsOn) != 1 || c.Spec.DependsOn[0] != "pet-medical/prod/db" {
		t.Errorf("dependsOn = %v", c.Spec.DependsOn)
	}
	if c.Spec.Schedule != "2026-06-09T02:00:00Z" {
		t.Errorf("schedule = %q", c.Spec.Schedule)
	}
}

func TestParse_RejectsUnsupportedVersion(t *testing.T) {
	bad := strings.Replace(validYAML, "rolloffs.klarlabs.de/v1", "rolloffs.klarlabs.de/v2", 1)
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatal("expected error for unsupported apiVersion, got nil")
	}
	if !strings.Contains(err.Error(), "apiVersion") {
		t.Errorf("error should mention apiVersion, got: %v", err)
	}
}

func TestParse_RejectsWrongKind(t *testing.T) {
	bad := strings.Replace(validYAML, "kind: RolloutConfig", "kind: Nonsense", 1)
	if _, err := Parse([]byte(bad)); err == nil {
		t.Fatal("expected error for wrong kind, got nil")
	}
}

func TestParse_RejectsMalformedYAML(t *testing.T) {
	if _, err := Parse([]byte("apiVersion: : :\n  bad")); err == nil {
		t.Fatal("expected error for malformed YAML, got nil")
	}
}

// The shipped example must always parse — guards against schema/example drift.
func TestParse_Example(t *testing.T) {
	data, err := os.ReadFile("../../examples/rollout-config.example.yaml")
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	if _, err := Parse(data); err != nil {
		t.Fatalf("example config must parse: %v", err)
	}
}

func TestSchemaJSON_Published(t *testing.T) {
	if len(SchemaJSON) == 0 {
		t.Fatal("SchemaJSON is empty; published schema must be embedded")
	}
	if !strings.Contains(string(SchemaJSON), SchemaVersion) {
		t.Errorf("embedded schema should reference the schema version %q", SchemaVersion)
	}
}
