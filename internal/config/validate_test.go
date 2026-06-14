package config

import (
	"strings"
	"testing"
)

func mustParse(t *testing.T, y string) *Config {
	t.Helper()
	c, err := Parse([]byte(y))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

func TestValidate_Valid(t *testing.T) {
	if err := Validate(mustParse(t, validYAML)); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

func TestValidate_AnalysisPluginRequiresPin(t *testing.T) {
	a := &Analysis{
		Provider:  "datadog",
		Plugin:    "/path/to/datadog",
		Metrics:   []AnalysisMetric{{Name: "errorRate", Query: "q"}},
		Condition: "errorRate < 0.05",
	}
	errs := validateAnalysis(a)
	if len(errs) == 0 {
		t.Fatal("analysis.plugin without sha256 must be rejected")
	}
	a.SHA256 = "deadbeef"
	if errs := validateAnalysis(a); len(errs) != 0 {
		t.Fatalf("pinned plugin analysis should validate, got %v", errs)
	}
}

func TestValidate_TrafficRoutingRequiredFields(t *testing.T) {
	errs := validateTrafficRouting(&TrafficRouting{})
	if len(errs) < 5 {
		t.Fatalf("empty trafficRouting should report missing plugin/sha256/route/services, got %v", errs)
	}
	ok := &TrafficRouting{Plugin: "p", SHA256: "s", Route: "r", StableService: "st", CanaryService: "ca"}
	if errs := validateTrafficRouting(ok); len(errs) != 0 {
		t.Fatalf("complete trafficRouting should validate, got %v", errs)
	}
}

func TestValidate_DatabaseHooks(t *testing.T) {
	// Empty migrate command is rejected with a database.migrate path.
	c := mustParse(t, validYAML)
	c.Spec.Database = &Database{Migrate: &DatabaseRollback{}}
	if err := Validate(c); err == nil || !strings.Contains(err.Error(), "database.migrate.command") {
		t.Errorf("empty migrate command must error, got %v", err)
	}
	// Bad timeout on the rollback hook is rejected.
	c = mustParse(t, validYAML)
	c.Spec.Database = &Database{Rollback: &DatabaseRollback{Command: []string{"goose", "down"}, Timeout: "nope"}}
	if err := Validate(c); err == nil || !strings.Contains(err.Error(), "database.rollback.timeout") {
		t.Errorf("bad rollback timeout must error, got %v", err)
	}
	// A complete database block validates.
	c = mustParse(t, validYAML)
	c.Spec.Database = &Database{
		Migrate:            &DatabaseRollback{Command: []string{"goose", "up"}, Timeout: "60s"},
		Rollback:           &DatabaseRollback{Command: []string{"goose", "down"}},
		BackwardCompatible: true,
	}
	if err := Validate(c); err != nil {
		t.Errorf("complete database block should validate, got %v", err)
	}
}

func TestSpec_DatabaseRollbackHookFallback(t *testing.T) {
	// Deprecated spec.rollback.database is honoured when spec.database.rollback is absent.
	s := Spec{Rollback: Rollback{Database: &DatabaseRollback{Command: []string{"legacy", "down"}}}}
	if got := s.DatabaseRollbackHook(); got == nil || got.Command[0] != "legacy" {
		t.Fatalf("fallback to rollback.database failed: %+v", got)
	}
	// spec.database.rollback takes precedence over the deprecated field.
	s.Database = &Database{Rollback: &DatabaseRollback{Command: []string{"new", "down"}}}
	if got := s.DatabaseRollbackHook(); got == nil || got.Command[0] != "new" {
		t.Fatalf("database.rollback should win: %+v", got)
	}
}

func TestValidate_VerificationEnum(t *testing.T) {
	c := mustParse(t, validYAML)
	for _, v := range []string{"", "shallow", "full"} {
		c.Spec.Verification = v
		if err := Validate(c); err != nil {
			t.Errorf("verification %q should be valid: %v", v, err)
		}
	}
	c.Spec.Verification = "deep"
	if err := Validate(c); err == nil || !strings.Contains(err.Error(), "verification") {
		t.Errorf("invalid verification must error, got %v", err)
	}
}

func TestValidate_MissingName(t *testing.T) {
	c := mustParse(t, validYAML)
	c.Metadata.Name = ""
	err := Validate(c)
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("expected name error, got: %v", err)
	}
}

func TestValidate_BadCriticality(t *testing.T) {
	c := mustParse(t, validYAML)
	c.Spec.Target.Criticality = "extreme"
	if err := Validate(c); err == nil {
		t.Fatal("expected error for invalid criticality")
	}
}

func TestValidate_BadStrategyType(t *testing.T) {
	c := mustParse(t, validYAML)
	c.Spec.Strategy.Type = "instant"
	if err := Validate(c); err == nil {
		t.Fatal("expected error for invalid strategy type")
	}
}

func TestValidate_HealthProbe_BothSet(t *testing.T) {
	c := mustParse(t, validYAML)
	c.Spec.Rollback.HealthCheck.TCP = "api.internal:443" // already has HTTP
	if err := Validate(c); err == nil {
		t.Fatal("expected error: health check must set exactly one of http/tcp/command")
	}
}

func TestValidate_HealthProbe_NoneSet(t *testing.T) {
	c := mustParse(t, validYAML)
	c.Spec.Rollback.HealthCheck.HTTP = ""
	if err := Validate(c); err == nil {
		t.Fatal("expected error: empty health check")
	}
}

func TestValidate_BadSchedule(t *testing.T) {
	c := mustParse(t, validYAML)
	c.Spec.Schedule = "tomorrow morning"
	err := Validate(c)
	if err == nil || !strings.Contains(err.Error(), "schedule") {
		t.Fatalf("expected schedule error, got: %v", err)
	}
}

func TestValidate_SelfDependency(t *testing.T) {
	c := mustParse(t, validYAML)
	c.Spec.DependsOn = []string{c.Spec.Target.Ref}
	err := Validate(c)
	if err == nil || !strings.Contains(err.Error(), "depend") {
		t.Fatalf("expected self-dependency error, got: %v", err)
	}
}

func TestValidate_CanaryNeedsSteps(t *testing.T) {
	c := mustParse(t, validYAML)
	c.Spec.Strategy.Steps = nil // canary with no steps
	err := Validate(c)
	if err == nil || !strings.Contains(err.Error(), "step") {
		t.Fatalf("expected canary-steps error, got: %v", err)
	}
}

func TestValidate_BadCELSensitive(t *testing.T) {
	c := mustParse(t, validYAML)
	c.Spec.Risk.Sensitive = `changeType ===` // syntax error
	err := Validate(c)
	if err == nil || !strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("expected risk.sensitive CEL error, got: %v", err)
	}
}

func TestValidate_BadCELTrigger(t *testing.T) {
	c := mustParse(t, validYAML)
	c.Spec.Rollback.Trigger = `mystery > 1` // unknown variable
	err := Validate(c)
	if err == nil || !strings.Contains(err.Error(), "trigger") {
		t.Fatalf("expected rollback.trigger CEL error, got: %v", err)
	}
}

func TestValidate_GoodCEL(t *testing.T) {
	c := mustParse(t, validYAML)
	c.Spec.Risk.Sensitive = `changeType == "schema" && environment == "prod"`
	c.Spec.Rollback.Trigger = `score > 0.9`
	if err := Validate(c); err != nil {
		t.Fatalf("well-formed CEL rejected: %v", err)
	}
}

// Load is the convenience path: parse + validate in one call.
func TestLoad_RejectsInvalid(t *testing.T) {
	bad := strings.Replace(validYAML, "criticality: high", "criticality: extreme", 1)
	if _, err := Load([]byte(bad)); err == nil {
		t.Fatal("Load must reject a schema-invalid config")
	}
}

func TestLoad_Valid(t *testing.T) {
	if _, err := Load([]byte(validYAML)); err != nil {
		t.Fatalf("Load rejected valid config: %v", err)
	}
}
