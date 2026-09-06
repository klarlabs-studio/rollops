package config

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const validYAML = `
apiVersion: rollops.klarlabs.de/v1
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
    history:
      lookback: 10
      weight: 0.15
      maxFailures: 3
  rollback:
    auto: true
    healthCheck:
      http: https://api.petmedical.internal/healthz
      timeout: 10s
    smokeTest:
      command: ["./smoke.sh"]
      expectExit: 0
    database:
      command: ["goose", "down"]
      timeout: 30s
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
	if c.Spec.Risk.History.Lookback != 10 || c.Spec.Risk.History.Weight != 0.15 || c.Spec.Risk.History.MaxFailures != 3 {
		t.Errorf("risk.history = %+v", c.Spec.Risk.History)
	}
	if c.Spec.Rollback.HealthCheck == nil || c.Spec.Rollback.SmokeTest == nil {
		t.Fatalf("rollback checks not parsed: %+v", c.Spec.Rollback)
	}
	if c.Spec.Rollback.Database == nil || strings.Join(c.Spec.Rollback.Database.Command, " ") != "goose down" || c.Spec.Rollback.Database.Timeout != "30s" {
		t.Fatalf("rollback.database = %+v", c.Spec.Rollback.Database)
	}
	if len(c.Spec.DependsOn) != 1 || c.Spec.DependsOn[0] != "pet-medical/prod/db" {
		t.Errorf("dependsOn = %v", c.Spec.DependsOn)
	}
	if c.Spec.Schedule != "2026-06-09T02:00:00Z" {
		t.Errorf("schedule = %q", c.Spec.Schedule)
	}
}

func TestLoad_DatabaseRollbackValidation(t *testing.T) {
	base := validYAML
	tests := []struct {
		name string
		old  string
		new  string
		want string
	}{
		{name: "empty command", old: `command: ["goose", "down"]`, new: `command: []`, want: "database.command"},
		{name: "bad timeout", old: "timeout: 30s", new: "timeout: nope", want: "database.timeout"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load([]byte(strings.Replace(base, tt.old, tt.new, 1)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestLoad_RiskHistoryValidation(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want string
	}{
		{name: "negative lookback", yaml: strings.Replace(validYAML, "lookback: 10", "lookback: -1", 1), want: "lookback"},
		{name: "negative weight", yaml: strings.Replace(validYAML, "weight: 0.15", "weight: -0.1", 1), want: "weight"},
		{name: "oversized weight", yaml: strings.Replace(validYAML, "weight: 0.15", "weight: 2", 1), want: "weight"},
		{name: "negative max failures", yaml: strings.Replace(validYAML, "maxFailures: 3", "maxFailures: -1", 1), want: "maxFailures"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load([]byte(tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestParse_RejectsUnsupportedVersion(t *testing.T) {
	bad := strings.Replace(validYAML, "rollops.klarlabs.de/v1", "rollops.klarlabs.de/v2", 1)
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

func TestLoad_AllExamples(t *testing.T) {
	var paths []string
	err := filepath.WalkDir("../../examples", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".yaml") {
			return nil
		}
		// Skip non-rollout YAML (RBAC snippets under agent-loop).
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Contains(data, []byte("apiVersion: rollops.klarlabs.de/")) {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk examples: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("expected at least one example config")
	}
	sort.Strings(paths)

	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read example: %v", err)
			}
			docs, err := LoadDocuments(data, path)
			if err != nil {
				t.Fatalf("example config must load and validate: %v", err)
			}
			if len(docs) == 0 {
				t.Fatal("expected at least one document")
			}
		})
	}
}

func TestLoad_AnalysisValidation(t *testing.T) {
	base := validYAML + `
  analysis:
    provider: prometheus
    address: http://prometheus:9090
    interval: 30s
    count: 2
    failureLimit: 1
    metrics:
      - name: errorRate
        query: rate(errors[1m])
    condition: errorRate < 0.05
`
	if _, err := Load([]byte(base)); err != nil {
		t.Fatalf("valid analysis should load: %v", err)
	}
	tests := []struct {
		name string
		old  string
		new  string
		want string
	}{
		{name: "unsupported provider", old: "provider: prometheus", new: "provider: datadog", want: "unsupported"},
		{name: "missing prometheus address", old: "address: http://prometheus:9090", new: "address: ''", want: "address"},
		{name: "bad interval", old: "interval: 30s", new: "interval: nope", want: "interval"},
		{name: "bad condition", old: "condition: errorRate < 0.05", new: "condition: errorRate <", want: "analysis"},
		{name: "bad aggregation", old: "query: rate(errors[1m])", new: "query: rate(errors[1m])\n        aggregation: median", want: "aggregation"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load([]byte(strings.Replace(base, tt.old, tt.new, 1)))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
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

func TestImagePolicy_WritebackMode(t *testing.T) {
	var nilPol *ImagePolicy
	if nilPol.WritebackMode() != WritebackPush {
		t.Error("nil policy must default to push")
	}
	if (&ImagePolicy{}).WritebackMode() != WritebackPush {
		t.Error("unset writeback must default to push")
	}
	if (&ImagePolicy{Writeback: "pull-request"}).WritebackMode() != WritebackPullRequest {
		t.Error("explicit pull-request not honoured")
	}
	if (&ImagePolicy{Writeback: "bogus"}).WritebackMode() != WritebackPush {
		t.Error("an unrecognised value must fall back to push, not silently disable writeback")
	}
}

func TestHonestStrategy(t *testing.T) {
	bake := &Config{}
	bake.Spec.Strategy.Type = "canary"
	if !strings.Contains(bake.HonestStrategy(), "health-gated bake") {
		t.Errorf("bare canary = %q, want health-gated bake", bake.HonestStrategy())
	}
	split := &Config{}
	split.Spec.Strategy.Type = "canary"
	split.Spec.TrafficRouting = &TrafficRouting{}
	if split.HonestStrategy() != "" {
		t.Errorf("canary with trafficRouting = %q, want empty", split.HonestStrategy())
	}
	flagged := &Config{}
	flagged.Spec.Strategy.Type = "canary"
	flagged.Spec.FeatureFlags = &FeatureFlags{}
	if flagged.HonestStrategy() != "" {
		t.Errorf("canary with featureFlags = %q, want empty", flagged.HonestStrategy())
	}
	cut := &Config{}
	cut.Spec.Strategy.Type = "blue-green"
	if !strings.Contains(cut.HonestStrategy(), "full cutover") {
		t.Errorf("bare blue-green = %q, want full cutover", cut.HonestStrategy())
	}
	routedBG := &Config{}
	routedBG.Spec.Strategy.Type = "blue-green"
	routedBG.Spec.TrafficRouting = &TrafficRouting{}
	if routedBG.HonestStrategy() != "" {
		t.Errorf("blue-green with trafficRouting = %q, want empty", routedBG.HonestStrategy())
	}
	rolling := &Config{}
	rolling.Spec.Strategy.Type = "rolling"
	if rolling.HonestStrategy() != "" {
		t.Errorf("rolling = %q, want empty", rolling.HonestStrategy())
	}
}

func TestLoad_ContinueOnFailure(t *testing.T) {
	c, err := Load([]byte(validYAML + "  continueOnFailure: true\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !c.Spec.ContinueOnFailure {
		t.Fatal("continueOnFailure not set")
	}
	c2, err := Load([]byte(validYAML))
	if err != nil {
		t.Fatalf("Load default: %v", err)
	}
	if c2.Spec.ContinueOnFailure {
		t.Fatal("continueOnFailure must default false")
	}
}
