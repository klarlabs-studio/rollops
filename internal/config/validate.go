package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// compiledSchema is the embedded SchemaJSON compiled once. Structural rules
// (required fields, enums, the health-probe oneOf) are enforced here so they
// stay declared in one place — the published schema — rather than duplicated
// in Go. Semantic rules the schema cannot express live in validateSemantics.
var (
	schemaOnce sync.Once
	schema     *jsonschema.Schema
	schemaErr  error
)

func loadSchema() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(SchemaJSON))
		if err != nil {
			schemaErr = fmt.Errorf("config: decode embedded schema: %w", err)
			return
		}
		c := jsonschema.NewCompiler()
		if err := c.AddResource("rolloffs.v1.json", doc); err != nil {
			schemaErr = fmt.Errorf("config: add schema resource: %w", err)
			return
		}
		schema, schemaErr = c.Compile("rolloffs.v1.json")
	})
	return schema, schemaErr
}

// Validate enforces the published schema plus the semantic rules the schema
// cannot express (RFC3339 schedule, no self-dependency, canary needs steps).
// It rejects loudly and aggregates every problem rather than failing on the
// first, so a human or agent sees the whole picture in one pass.
func Validate(c *Config) error {
	var errs []error

	sch, err := loadSchema()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("config: marshal for validation: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("config: decode for validation: %w", err)
	}
	if err := sch.Validate(inst); err != nil {
		errs = append(errs, fmt.Errorf("config: schema: %w", err))
	}

	errs = append(errs, validateSemantics(c)...)
	return errors.Join(errs...)
}

func validateSemantics(c *Config) []error {
	var errs []error

	if c.Spec.Schedule != "" {
		if _, err := time.Parse(time.RFC3339, c.Spec.Schedule); err != nil {
			errs = append(errs, fmt.Errorf("config: schedule %q is not RFC3339: %w", c.Spec.Schedule, err))
		}
	}

	for _, dep := range c.Spec.DependsOn {
		if dep == c.Spec.Target.Ref {
			errs = append(errs, fmt.Errorf("config: target %q cannot depend on itself", dep))
		}
	}

	errs = append(errs, validateStrategy("spec.strategy", c.Spec.Strategy)...)
	for i, env := range c.Spec.Environments {
		if env.Strategy != nil {
			errs = append(errs, validateStrategy(fmt.Sprintf("environments[%d].strategy", i), *env.Strategy)...)
		}
	}
	return errs
}

func validateStrategy(path string, s Strategy) []error {
	var errs []error
	if s.Type == "canary" && len(s.Steps) == 0 {
		errs = append(errs, fmt.Errorf("config: %s: canary strategy requires at least one step", path))
	}
	return errs
}

// Load parses and validates in one call — the standard entry point. Parse alone
// is structural (version/kind gate, unknown-field rejection); Load adds full
// schema + semantic validation.
func Load(data []byte) (*Config, error) {
	c, err := Parse(data)
	if err != nil {
		return nil, err
	}
	if err := Validate(c); err != nil {
		return nil, err
	}
	return c, nil
}
