// Package config is the YAML surface of Rolloffs: the declarative format
// fluently written by both humans and agents, backed by a strict published
// schema (schema/rolloffs.v1.schema.json, embedded as SchemaJSON) and a schema
// version field so configs migrate cleanly as the format evolves.
//
// This package owns structural parsing and version gating. Deep semantic
// validation (required fields, enum membership, CEL well-formedness) lives in
// the validation layer; loud rejection of malformed config is its job, not this
// one's. Conditional logic fields (risk.sensitive, rollback.trigger) carry CEL
// expressions evaluated downstream, never a bespoke DSL.
package config

import (
	"bytes"
	_ "embed"
	"fmt"

	"gopkg.in/yaml.v3"
)

// SchemaVersion is the only apiVersion this build accepts. The version lives in
// the config surface (apiVersion) so a future v2 can be detected and migrated
// rather than silently mis-parsed.
const SchemaVersion = "rolloffs.klarlabs.de/v1"

// Kind is the single document kind in v1.
const Kind = "RolloutConfig"

// SchemaJSON is the published JSON Schema for the config surface, embedded so
// it ships with the binary and the validation layer can enforce it offline.
//
//go:embed schema/rolloffs.v1.schema.json
var SchemaJSON []byte

// Config is a parsed rollout configuration — one per service/repo.
type Config struct {
	APIVersion string   `yaml:"apiVersion" json:"apiVersion"`
	Kind       string   `yaml:"kind" json:"kind"`
	Metadata   Metadata `yaml:"metadata" json:"metadata"`
	Spec       Spec     `yaml:"spec" json:"spec"`
}

// Metadata identifies the config.
type Metadata struct {
	Name   string            `yaml:"name" json:"name"`
	Labels map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
}

// Spec is the desired rollout behaviour for the target.
type Spec struct {
	Target       Target        `yaml:"target" json:"target"`
	Environments []Environment `yaml:"environments,omitempty" json:"environments,omitempty"`
	Strategy     Strategy      `yaml:"strategy" json:"strategy"`
	Risk         Risk          `yaml:"risk,omitempty" json:"risk,omitempty"`
	Rollback     Rollback      `yaml:"rollback,omitempty" json:"rollback,omitempty"`
	DependsOn    []string      `yaml:"dependsOn,omitempty" json:"dependsOn,omitempty"`
	Schedule     string        `yaml:"schedule,omitempty" json:"schedule,omitempty"` // RFC3339 future time
}

// Target selects the deployment target plugin and its criticality weight.
type Target struct {
	Kind        string         `yaml:"kind" json:"kind"`               // ssh | ftp | kubernetes | <plugin>
	Ref         string         `yaml:"ref" json:"ref"`                 // stable target identity
	Criticality string         `yaml:"criticality" json:"criticality"` // low | medium | high | critical
	Spec        map[string]any `yaml:"spec,omitempty" json:"spec,omitempty"`
}

// Environment is a promotion stage; Strategy overrides the spec default.
type Environment struct {
	Name     string    `yaml:"name" json:"name"` // dev | staging | prod | <custom>
	Promote  bool      `yaml:"promote,omitempty" json:"promote,omitempty"`
	Strategy *Strategy `yaml:"strategy,omitempty" json:"strategy,omitempty"`
}

// Strategy is the progressive-delivery shape.
type Strategy struct {
	Type  string         `yaml:"type" json:"type"` // rolling | canary | blue-green
	Steps []StrategyStep `yaml:"steps,omitempty" json:"steps,omitempty"`
}

// StrategyStep is one traffic-shifting increment.
type StrategyStep struct {
	Weight int    `yaml:"weight,omitempty" json:"weight,omitempty"` // canary traffic percent
	Pause  string `yaml:"pause,omitempty" json:"pause,omitempty"`   // duration between steps
}

// Risk configures the decision-kit gate. Sensitive is a CEL expression that,
// when true, forces human approval regardless of the computed score.
type Risk struct {
	Threshold float64 `yaml:"threshold,omitempty" json:"threshold,omitempty"`
	Sensitive string  `yaml:"sensitive,omitempty" json:"sensitive,omitempty"` // CEL bool
}

// Rollback configures auto-rollback. Trigger is an optional CEL expression;
// the built-in observability-free signals are HealthCheck and SmokeTest.
type Rollback struct {
	Auto        bool         `yaml:"auto,omitempty" json:"auto,omitempty"`
	HealthCheck *HealthCheck `yaml:"healthCheck,omitempty" json:"healthCheck,omitempty"`
	SmokeTest   *SmokeTest   `yaml:"smokeTest,omitempty" json:"smokeTest,omitempty"`
	Trigger     string       `yaml:"trigger,omitempty" json:"trigger,omitempty"` // CEL bool
}

// HealthCheck is an observability-free liveness probe (exactly one of HTTP/TCP/Command).
type HealthCheck struct {
	HTTP    string   `yaml:"http,omitempty" json:"http,omitempty"`
	TCP     string   `yaml:"tcp,omitempty" json:"tcp,omitempty"`
	Command []string `yaml:"command,omitempty" json:"command,omitempty"`
	Timeout string   `yaml:"timeout,omitempty" json:"timeout,omitempty"`
}

// SmokeTest runs a command after deploy and expects ExpectExit.
type SmokeTest struct {
	Command    []string `yaml:"command" json:"command"`
	ExpectExit int      `yaml:"expectExit,omitempty" json:"expectExit,omitempty"`
}

// Parse decodes YAML into a Config and gates the schema version and kind. It
// rejects malformed YAML and an unsupported apiVersion/kind loudly, but does
// not perform deep semantic validation — that is the validation layer's job.
func Parse(data []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // reject unknown fields — no silent typos
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("config: parse: %w", err)
	}
	if c.APIVersion != SchemaVersion {
		return nil, fmt.Errorf("config: unsupported apiVersion %q (this build supports %q)", c.APIVersion, SchemaVersion)
	}
	if c.Kind != Kind {
		return nil, fmt.Errorf("config: unsupported kind %q (want %q)", c.Kind, Kind)
	}
	return &c, nil
}
