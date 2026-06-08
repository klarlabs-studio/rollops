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
