package environment

import (
	"errors"
	"strings"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/value"
)

var at = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

const projectID = identity.ProjectID("prj_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d002")

func valid() Environment {
	return Environment{
		ID:        "env_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d003",
		ProjectID: projectID,
		Name:      "production",
		Kind:      KindProduction,
		Targets: []TargetBinding{{
			Name:   "primary",
			Driver: "kubernetes",
			Config: map[string]value.Ref{
				"kubeconfig": value.Secret("prod/kubeconfig"),
				"namespace":  value.Literal("payments"),
			},
		}},
	}
}

func TestValidEnvironment(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestEnvironmentRejectsAnIncompleteRecord(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Environment)
	}{
		{"no id", func(e *Environment) { e.ID = "" }},
		{"foreign id", func(e *Environment) { e.ID = "prj_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d003" }},
		{"no project", func(e *Environment) { e.ProjectID = "" }},
		{"foreign project id", func(e *Environment) { e.ProjectID = "env_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d002" }},
		{"no name", func(e *Environment) { e.Name = " " }},
		{"bad name", func(e *Environment) { e.Name = "Production" }},
		{"no kind", func(e *Environment) { e.Kind = "" }},
		{"invented kind", func(e *Environment) { e.Kind = "prodlike" }},
		{"negative ttl", func(e *Environment) { e.Lifecycle.TTL = -time.Hour }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := valid()
			c.mutate(&e)
			if err := e.Validate(); err == nil {
				t.Error("Validate() = nil, want an error")
			}
		})
	}
}

// An environment with no target cannot deploy anything, but it is a legitimate
// record to create before its infrastructure exists — so it validates.
func TestAnEnvironmentMayHaveNoTargetsYet(t *testing.T) {
	e := valid()
	e.Targets = nil
	if err := e.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
	if e.CanDeploy() {
		t.Error("CanDeploy() = true with no target bound")
	}
	if !valid().CanDeploy() {
		t.Error("CanDeploy() = false with a target bound")
	}
}

// A plan names the target it lands on, so two bindings sharing a name would
// make the choice depend on iteration order.
func TestTargetNamesMustBeUniqueWithinAnEnvironment(t *testing.T) {
	e := valid()
	e.Targets = append(e.Targets, TargetBinding{
		Name:   "primary",
		Driver: "ssh",
		Config: map[string]value.Ref{"host": value.Literal("10.0.0.1")},
	})
	if err := e.Validate(); !errors.Is(err, ErrDuplicateTarget) {
		t.Errorf("Validate() = %v, want ErrDuplicateTarget", err)
	}
}

func TestPolicyNamesMustBeUniqueWithinAnEnvironment(t *testing.T) {
	e := valid()
	e.Policies = []PolicyBinding{
		{Name: "signed-artifacts", Ref: "policies/signed.cel", Mode: PolicyEnforce},
		{Name: "signed-artifacts", Ref: "policies/other.cel", Mode: PolicyWarn},
	}
	if err := e.Validate(); !errors.Is(err, ErrDuplicatePolicy) {
		t.Errorf("Validate() = %v, want ErrDuplicatePolicy", err)
	}
}

func TestTargetBindingRejectsAnIncompleteRecord(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*TargetBinding)
	}{
		{"no name", func(b *TargetBinding) { b.Name = "" }},
		{"bad name", func(b *TargetBinding) { b.Name = "Primary" }},
		{"no driver", func(b *TargetBinding) { b.Driver = " " }},
		{"unset config value", func(b *TargetBinding) { b.Config = map[string]value.Ref{"host": {}} }},
		{"blank config key", func(b *TargetBinding) { b.Config = map[string]value.Ref{" ": value.Literal("x")} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := valid().Targets[0]
			c.mutate(&b)
			if err := b.Validate(); err == nil {
				t.Error("Validate() = nil, want an error")
			}
		})
	}
}

// ADR-0002: the driver is a plain string resolved by a registry, so the domain
// accepts any plausible one rather than holding the list of every plugin.
func TestAnyPlausibleDriverIsAccepted(t *testing.T) {
	for _, driver := range []string{"kubernetes", "ssh", "ftp", "some-future-plugin"} {
		b := valid().Targets[0]
		b.Driver = driver
		if err := b.Validate(); err != nil {
			t.Errorf("Validate() = %v for driver %q, want nil", err, driver)
		}
	}
}

func TestPolicyBindingRejectsAnIncompleteRecord(t *testing.T) {
	cases := []struct {
		name string
		in   PolicyBinding
	}{
		{"no name", PolicyBinding{Ref: "policies/signed.cel", Mode: PolicyEnforce}},
		{"no ref", PolicyBinding{Name: "signed", Mode: PolicyEnforce}},
		{"no mode", PolicyBinding{Name: "signed", Ref: "policies/signed.cel"}},
		{"invented mode", PolicyBinding{Name: "signed", Ref: "policies/signed.cel", Mode: "audit"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.in.Validate(); err == nil {
				t.Error("Validate() = nil, want an error")
			}
		})
	}
}

// A binding that does not validate on its own must not validate because the
// environment around it does.
func TestAnEnvironmentRejectsABrokenBinding(t *testing.T) {
	t.Run("target", func(t *testing.T) {
		e := valid()
		e.Targets[0].Driver = ""
		if err := e.Validate(); err == nil {
			t.Error("Validate() = nil, want an error")
		}
	})
	t.Run("policy", func(t *testing.T) {
		e := valid()
		e.Policies = []PolicyBinding{{Name: "signed-artifacts", Ref: ""}}
		if err := e.Validate(); err == nil {
			t.Error("Validate() = nil, want an error")
		}
	})
	t.Run("blank variable name", func(t *testing.T) {
		e := valid()
		e.Variables = map[string]value.Ref{" ": value.Literal("x")}
		if err := e.Validate(); err == nil {
			t.Error("Validate() = nil, want an error")
		}
	})
}

func TestAPolicyMayBeBoundInEitherMode(t *testing.T) {
	for _, mode := range []PolicyMode{PolicyEnforce, PolicyWarn} {
		e := valid()
		e.Policies = []PolicyBinding{{Name: "signed-artifacts", Ref: "policies/signed.cel", Mode: mode}}
		if err := e.Validate(); err != nil {
			t.Errorf("Validate() = %v for mode %q, want nil", err, mode)
		}
	}
}

func TestStringSaysSoWhenNothingIsBound(t *testing.T) {
	e := valid()
	e.Targets = nil
	if got := e.String(); !strings.Contains(got, "no targets") {
		t.Errorf("String() = %q, want it to say there are no targets", got)
	}
}

func TestNewStampsIdentity(t *testing.T) {
	g := identity.NewFixedGenerator("11111111-1111-7111-8111-111111111111")
	e, err := New(g, Environment{ProjectID: projectID, Name: "staging", Kind: KindStaging})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if want := identity.EnvironmentID("env_11111111-1111-7111-8111-111111111111"); e.ID != want {
		t.Errorf("ID = %q, want %q", e.ID, want)
	}
}

func TestNewRefusesAnInvalidEnvironment(t *testing.T) {
	g := identity.NewGenerator()
	if _, err := New(g, Environment{ProjectID: projectID, Name: "staging"}); err == nil {
		t.Error("New accepted an environment with no kind")
	}
}

func TestNewPropagatesAGeneratorFailure(t *testing.T) {
	boom := errors.New("no entropy")
	g := identity.GeneratorFunc(func() (string, error) { return "", boom })
	if _, err := New(g, valid()); !errors.Is(err, boom) {
		t.Errorf("got %v, want the generator's error", err)
	}
}

func TestTargetLookupByName(t *testing.T) {
	e := valid()
	got, ok := e.Target("primary")
	if !ok {
		t.Fatal("Target(\"primary\") not found")
	}
	if got.Driver != "kubernetes" {
		t.Errorf("Driver = %q", got.Driver)
	}
	if _, ok := e.Target("absent"); ok {
		t.Error("Target(\"absent\") = true")
	}
}

// INV-011: an environment is persisted and rendered on every surface, so a
// credential in its target configuration must travel as a reference.
func TestEnvironmentConfigurationNeverRendersSecretMaterial(t *testing.T) {
	e := valid()
	resolved, err := e.Targets[0].Config["kubeconfig"].Resolve(fakeResolver{})
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "the-real-kubeconfig" {
		t.Fatalf("resolve = %q", resolved)
	}
	for _, rendered := range []string{
		e.String(),
		e.Targets[0].String(),
	} {
		if strings.Contains(rendered, "the-real-kubeconfig") {
			t.Errorf("a rendering leaked secret material: %q", rendered)
		}
	}
}

func TestStringNamesTheEnvironmentAndItsTargets(t *testing.T) {
	got := valid().String()
	for _, want := range []string{"production", "primary", "kubernetes"} {
		if !strings.Contains(got, want) {
			t.Errorf("String() = %q, want it to mention %q", got, want)
		}
	}
}

// Production is the environment a policy most needs to distinguish, and doing
// so by comparing the name would break for an environment named "prod".
func TestKindIdentifiesProductionRatherThanTheName(t *testing.T) {
	e := valid()
	if !e.IsProduction() {
		t.Error("IsProduction() = false for a production environment")
	}
	e.Name = "prod"
	if !e.IsProduction() {
		t.Error("IsProduction() followed the name rather than the kind")
	}
	e.Kind = KindStaging
	if e.IsProduction() {
		t.Error("IsProduction() = true for staging")
	}
}

func TestLifecycleExpiry(t *testing.T) {
	cases := []struct {
		name      string
		lifecycle Lifecycle
		asOf      time.Time
		expired   bool
	}{
		{"no ttl never expires", Lifecycle{}, at.Add(10000 * time.Hour), false},
		{"before ttl", Lifecycle{TTL: time.Hour}, at.Add(time.Minute), false},
		{"after ttl", Lifecycle{TTL: time.Hour}, at.Add(2 * time.Hour), true},
		{"exactly at ttl", Lifecycle{TTL: time.Hour}, at.Add(time.Hour), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.lifecycle.ExpiredAt(at, c.asOf); got != c.expired {
				t.Errorf("ExpiredAt() = %v, want %v", got, c.expired)
			}
		})
	}
}

func TestEveryKindIsAccepted(t *testing.T) {
	for _, k := range []Kind{KindDevelopment, KindPreview, KindStaging, KindProduction, KindCustom} {
		e := valid()
		e.Kind = k
		if err := e.Validate(); err != nil {
			t.Errorf("Validate() = %v for kind %q", err, k)
		}
	}
}

func TestVariablesMustBeResolvable(t *testing.T) {
	e := valid()
	e.Variables = map[string]value.Ref{"REGION": {}}
	if err := e.Validate(); err == nil {
		t.Error("Validate() accepted an unset variable")
	}

	e.Variables = map[string]value.Ref{"REGION": value.Literal("eu-central-1")}
	if err := e.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

type fakeResolver struct{}

func (fakeResolver) ResolveSecret(string) (string, error) { return "the-real-kubeconfig", nil }
