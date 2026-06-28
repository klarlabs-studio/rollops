package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"go.klarlabs.de/rollops/internal/analysis"
	"go.klarlabs.de/rollops/internal/condition"
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
		if err := c.AddResource("rollops.v1.json", doc); err != nil {
			schemaErr = fmt.Errorf("config: add schema resource: %w", err)
			return
		}
		schema, schemaErr = c.Compile("rollops.v1.json")
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

	// CEL conditions must be well-formed bool expressions over the rollout
	// decision variables (internal/condition). Reject malformed logic at config
	// load time, not at rollout time.
	if c.Spec.Risk.Sensitive != "" {
		if err := condition.Check(c.Spec.Risk.Sensitive); err != nil {
			errs = append(errs, fmt.Errorf("config: risk.sensitive: %w", err))
		}
	}
	errs = append(errs, validateRiskHistory(c.Spec.Risk.History)...)
	if c.Spec.Rollback.Trigger != "" {
		if err := condition.Check(c.Spec.Rollback.Trigger); err != nil {
			errs = append(errs, fmt.Errorf("config: rollback.trigger: %w", err))
		}
	}
	if c.Spec.Rollback.Database != nil {
		errs = append(errs, validateDatabaseCommand("rollback.database", c.Spec.Rollback.Database)...)
	}
	if c.Spec.Database != nil {
		if m := c.Spec.Database.Migrate; m != nil {
			errs = append(errs, validateDatabaseCommand("database.migrate", m)...)
			switch m.When {
			case "", MigratePreDeploy, MigratePostPromote:
			default:
				errs = append(errs, fmt.Errorf("config: database.migrate.when %q must be %s | %s", m.When, MigratePreDeploy, MigratePostPromote))
			}
		}
		if rb := c.Spec.Database.Rollback; rb != nil {
			errs = append(errs, validateDatabaseCommand("database.rollback", rb)...)
			if rb.When != "" {
				errs = append(errs, fmt.Errorf("config: database.rollback.when is not valid; when applies only to database.migrate"))
			}
		}
	}
	if c.Spec.Analysis != nil {
		errs = append(errs, validateAnalysis(c.Spec.Analysis)...)
	}
	if c.Spec.FeatureFlags != nil {
		errs = append(errs, validateFeatureFlags(c.Spec.FeatureFlags)...)
	}
	if c.Spec.TrafficRouting != nil {
		errs = append(errs, validateTrafficRouting(c.Spec.TrafficRouting)...)
	}
	switch c.Spec.Verification {
	case "", "shallow", "detect", "full":
	default:
		errs = append(errs, fmt.Errorf("config: verification %q must be shallow | detect | full", c.Spec.Verification))
	}
	if c.Spec.ImagePolicy != nil {
		switch c.Spec.ImagePolicy.Mode {
		case "none", "major", "minor", "patch", "any", "digest":
		default:
			errs = append(errs, fmt.Errorf("config: imagePolicy.mode %q must be none | major | minor | patch | any | digest", c.Spec.ImagePolicy.Mode))
		}
		if img, _ := c.Spec.Target.Spec["image"].(string); img == "" {
			errs = append(errs, fmt.Errorf("config: imagePolicy requires spec.target.spec.image (the tracked image)"))
		}
	}
	return errs
}

func validateTrafficRouting(t *TrafficRouting) []error {
	var errs []error
	switch t.Provider {
	case "":
		// Plugin mode: a pinned binary is required.
		if t.Plugin == "" {
			errs = append(errs, fmt.Errorf("config: trafficRouting.plugin (binary path) is required when no provider is set"))
		}
		if t.SHA256 == "" {
			errs = append(errs, fmt.Errorf("config: trafficRouting.sha256 pin is required when no provider is set"))
		}
	case "gateway":
		// Built-in router: no plugin needed.
	default:
		errs = append(errs, fmt.Errorf("config: trafficRouting.provider %q must be gateway (or empty for a plugin)", t.Provider))
	}
	if t.Route == "" {
		errs = append(errs, fmt.Errorf("config: trafficRouting.route is required"))
	}
	if t.StableService == "" {
		errs = append(errs, fmt.Errorf("config: trafficRouting.stableService is required"))
	}
	if t.CanaryService == "" {
		errs = append(errs, fmt.Errorf("config: trafficRouting.canaryService is required"))
	}
	return errs
}

func validateFeatureFlags(f *FeatureFlags) []error {
	var errs []error
	if f.Plugin == "" {
		errs = append(errs, fmt.Errorf("config: featureFlags.plugin (binary path) is required"))
	}
	if f.SHA256 == "" {
		errs = append(errs, fmt.Errorf("config: featureFlags.sha256 pin is required"))
	}
	if f.Flag == "" {
		errs = append(errs, fmt.Errorf("config: featureFlags.flag is required"))
	}
	switch f.When {
	case "", "step", "promote", "both":
	default:
		errs = append(errs, fmt.Errorf("config: featureFlags.when %q must be step | promote | both", f.When))
	}
	return errs
}

// validateDatabaseCommand checks a database command hook (forward migrate or
// rollback), reporting errors under the given config path for clear messages.
func validateDatabaseCommand(path string, db *DatabaseRollback) []error {
	var errs []error
	if len(db.Command) == 0 {
		errs = append(errs, fmt.Errorf("config: %s.command must not be empty", path))
	}
	if db.Timeout != "" {
		if _, err := time.ParseDuration(db.Timeout); err != nil {
			errs = append(errs, fmt.Errorf("config: %s.timeout %q is not a Go duration: %w", path, db.Timeout, err))
		}
	}
	return errs
}

func validateRiskHistory(h RiskHistory) []error {
	var errs []error
	if h.Lookback < 0 {
		errs = append(errs, fmt.Errorf("config: risk.history.lookback must be >= 0"))
	}
	if h.Weight < 0 || h.Weight > 1 {
		errs = append(errs, fmt.Errorf("config: risk.history.weight must be between 0 and 1"))
	}
	if h.MaxFailures < 0 {
		errs = append(errs, fmt.Errorf("config: risk.history.maxFailures must be >= 0"))
	}
	return errs
}

func validateAnalysis(a *Analysis) []error {
	var errs []error
	if a.Plugin != "" {
		// A metricprovider plugin supplies the backend; the provider name is the
		// plugin's own and the binary must be pinned.
		if a.SHA256 == "" {
			errs = append(errs, fmt.Errorf("config: analysis.sha256 pin is required when analysis.plugin is set"))
		}
	} else {
		if a.Provider != "prometheus" {
			errs = append(errs, fmt.Errorf("config: analysis.provider %q is unsupported without a plugin (built-in: prometheus; else set analysis.plugin)", a.Provider))
		}
		if a.Provider == "prometheus" && strings.TrimSpace(a.Address) == "" {
			errs = append(errs, fmt.Errorf("config: analysis.address is required for prometheus"))
		}
	}
	if a.Interval != "" {
		if _, err := time.ParseDuration(a.Interval); err != nil {
			errs = append(errs, fmt.Errorf("config: analysis.interval %q is not a Go duration: %w", a.Interval, err))
		}
	}
	metrics := make([]analysis.Metric, 0, len(a.Metrics))
	for _, m := range a.Metrics {
		metrics = append(metrics, analysis.Metric{Name: m.Name, Query: m.Query})
	}
	if _, err := analysis.New(nil, analysis.Template{
		Metrics:      metrics,
		Condition:    a.Condition,
		Count:        a.Count,
		FailureLimit: a.FailureLimit,
	}); err != nil {
		errs = append(errs, fmt.Errorf("config: analysis: %w", err))
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
