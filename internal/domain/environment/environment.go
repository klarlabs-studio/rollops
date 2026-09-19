// Package environment models a place a release can be deployed to.
//
// An environment is a real domain entity, not a string (spec 4.4). Its
// bindings are inert references: a target binding names a driver and
// configures it but does not contain it, so the environment stays comparable,
// serializable and free of provider dependencies (ADR-0002, INV-007).
package environment

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/name"
	"go.klarlabs.de/rollops/internal/domain/value"
)

var (
	// ErrDuplicateTarget reports two target bindings claiming one name, which
	// would make the target a plan lands on depend on iteration order.
	ErrDuplicateTarget = errors.New("environment: duplicate target binding")

	// ErrDuplicatePolicy reports the same policy bound twice.
	ErrDuplicatePolicy = errors.New("environment: duplicate policy binding")
)

// Kind is what an environment is for. Policy distinguishes environments by
// kind rather than by name, so that an environment named "prod" is not treated
// as production by accident — or a production one missed because it is not.
type Kind string

const (
	KindDevelopment Kind = "development"
	KindPreview     Kind = "preview"
	KindStaging     Kind = "staging"
	KindProduction  Kind = "production"
	KindCustom      Kind = "custom"
)

func (k Kind) valid() bool {
	switch k {
	case KindDevelopment, KindPreview, KindStaging, KindProduction, KindCustom:
		return true
	default:
		return false
	}
}

// PolicyMode is whether a bound policy blocks or reports.
type PolicyMode string

const (
	PolicyEnforce PolicyMode = "enforce"
	PolicyWarn    PolicyMode = "warn"
)

// TargetBinding names a deployment substrate and configures it. The driver is
// a plain string resolved through a registry in the adapter layer: a typed
// enum here would make the domain the list of every plugin that will ever
// exist (ADR-0002).
type TargetBinding struct {
	Name   string
	Driver string
	Config map[string]value.Ref
	Labels map[string]string
}

// Validate reports whether the binding can be resolved to a target.
func (b TargetBinding) Validate() error {
	if err := name.Validate("target binding", b.Name); err != nil {
		return fmt.Errorf("environment: %w", err)
	}
	if strings.TrimSpace(b.Driver) == "" {
		return fmt.Errorf("environment: target binding %q names no driver", b.Name)
	}
	for key, ref := range b.Config {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("environment: target binding %q has a blank config key", b.Name)
		}
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("environment: target binding %q: config %q: %w", b.Name, key, err)
		}
	}
	return nil
}

// String renders the binding. Configuration is deliberately omitted: a target
// is configured with credentials, and a binding is rendered wherever an
// environment is (INV-011).
func (b TargetBinding) String() string {
	return fmt.Sprintf("%s (%s)", b.Name, b.Driver)
}

// PolicyBinding puts a policy in force for an environment. Where the policy
// applies is not bound here — the decision point reaches the engine as input,
// and expressing it twice would give two places to disagree (ADR-0002).
type PolicyBinding struct {
	Name string
	Ref  string
	Mode PolicyMode
}

// Validate reports whether the binding names a policy that can be evaluated.
func (p PolicyBinding) Validate() error {
	if err := name.Validate("policy binding", p.Name); err != nil {
		return fmt.Errorf("environment: %w", err)
	}
	if strings.TrimSpace(p.Ref) == "" {
		return fmt.Errorf("environment: policy binding %q references no policy", p.Name)
	}
	if p.Mode != PolicyEnforce && p.Mode != PolicyWarn {
		return fmt.Errorf("environment: policy binding %q has mode %q", p.Name, p.Mode)
	}
	return nil
}

// Lifecycle is how long an environment is meant to last. A preview
// environment expires; a production one does not, which is the zero value.
type Lifecycle struct {
	TTL           time.Duration
	DeleteOnClose bool
}

// ExpiredAt reports whether an environment created at created has outlived its
// TTL by asOf. Creation time is passed in rather than held on the environment
// because an environment record carries no timestamps (spec 4.4).
func (l Lifecycle) ExpiredAt(created, asOf time.Time) bool {
	if l.TTL <= 0 {
		return false
	}
	return asOf.After(created.Add(l.TTL))
}

// Environment is a place a release can be deployed to.
type Environment struct {
	ID        identity.EnvironmentID
	ProjectID identity.ProjectID
	Name      string
	Kind      Kind
	Targets   []TargetBinding
	Policies  []PolicyBinding
	Variables map[string]value.Ref
	Labels    map[string]string
	Lifecycle Lifecycle
}

// New stamps identity onto e, then validates it.
func New(g identity.Generator, e Environment) (Environment, error) {
	id, err := identity.NewEnvironmentID(g)
	if err != nil {
		return Environment{}, err
	}
	e.ID = id
	if err := e.Validate(); err != nil {
		return Environment{}, err
	}
	return e, nil
}

// Validate reports whether the environment is a complete, unambiguous record.
func (e Environment) Validate() error {
	if _, err := identity.ParseEnvironmentID(string(e.ID)); err != nil {
		return fmt.Errorf("environment: id: %w", err)
	}
	if _, err := identity.ParseProjectID(string(e.ProjectID)); err != nil {
		return fmt.Errorf("environment: project id: %w", err)
	}
	if err := name.Validate("environment", e.Name); err != nil {
		return fmt.Errorf("environment: %w", err)
	}
	if !e.Kind.valid() {
		return fmt.Errorf("environment: %q is not an environment kind", e.Kind)
	}
	if e.Lifecycle.TTL < 0 {
		return fmt.Errorf("environment: ttl %s is negative", e.Lifecycle.TTL)
	}
	seenTargets := make(map[string]struct{}, len(e.Targets))
	for _, t := range e.Targets {
		if err := t.Validate(); err != nil {
			return err
		}
		if _, dup := seenTargets[t.Name]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicateTarget, t.Name)
		}
		seenTargets[t.Name] = struct{}{}
	}
	seenPolicies := make(map[string]struct{}, len(e.Policies))
	for _, p := range e.Policies {
		if err := p.Validate(); err != nil {
			return err
		}
		if _, dup := seenPolicies[p.Name]; dup {
			return fmt.Errorf("%w: %q", ErrDuplicatePolicy, p.Name)
		}
		seenPolicies[p.Name] = struct{}{}
	}
	for key, ref := range e.Variables {
		if strings.TrimSpace(key) == "" {
			return errors.New("environment: blank variable name")
		}
		if err := ref.Validate(); err != nil {
			return fmt.Errorf("environment: variable %q: %w", key, err)
		}
	}
	return nil
}

// Target returns the binding called n, if the environment has one.
func (e Environment) Target(n string) (TargetBinding, bool) {
	for _, t := range e.Targets {
		if t.Name == n {
			return t, true
		}
	}
	return TargetBinding{}, false
}

// CanDeploy reports whether the environment has anywhere to deploy to. An
// environment with no target is a legitimate record — it may be created before
// its infrastructure exists — but nothing can land in it.
func (e Environment) CanDeploy() bool { return len(e.Targets) > 0 }

// IsProduction reports whether policy should treat this as production.
func (e Environment) IsProduction() bool { return e.Kind == KindProduction }

// String renders the environment. Like TargetBinding.String it names what is
// bound, never how it is configured.
func (e Environment) String() string {
	if len(e.Targets) == 0 {
		return fmt.Sprintf("%s (%s): no targets", e.Name, e.Kind)
	}
	targets := make([]string, 0, len(e.Targets))
	for _, t := range e.Targets {
		targets = append(targets, t.String())
	}
	return fmt.Sprintf("%s (%s): %s", e.Name, e.Kind, strings.Join(targets, ", "))
}
