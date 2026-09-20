package environment

import (
	"context"
	"encoding/json"
	"fmt"

	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
)

// environmentCreated says where an environment sits and what it was wired to.
//
// Neither the target configuration nor the variables are here, and not only
// their values: their keys are absent too. A value.Ref never holds secret
// material, so nothing could copy one out — but a list of configuration keys is
// an inventory of how the estate holds its credentials, and the log is the one
// record that cannot be redacted afterwards (INV-012, INV-013). It is the same
// reason environment.TargetBinding.String omits them. The environment record
// keeps them for whoever asks for that environment.
type environmentCreated struct {
	ProjectID identity.ProjectID `json:"project_id"`
	Name      string             `json:"name"`
	Kind      environment.Kind   `json:"kind"`

	// Targets and Policies are what an auditor reads this entry for: which
	// substrate the environment was pointed at, and what was in force over it.
	Targets  []targetRef `json:"targets,omitempty"`
	Policies []policyRef `json:"policies,omitempty"`
}

type targetRef struct {
	Name   string `json:"name"`
	Driver string `json:"driver"`
}

type policyRef struct {
	Name string `json:"name"`
	Mode string `json:"mode"`
}

// record files the creation against the environment. Like a project it starts
// its own chain: declaring a place to deploy to is not caused by anything this
// system did.
func (s *Service) record(
	ctx context.Context, by identity.Principal, e environment.Environment,
) error {
	rec := environmentCreated{ProjectID: e.ProjectID, Name: e.Name, Kind: e.Kind}
	for _, t := range e.Targets {
		rec.Targets = append(rec.Targets, targetRef{Name: t.Name, Driver: t.Driver})
	}
	for _, p := range e.Policies {
		rec.Policies = append(rec.Policies, policyRef{Name: p.Name, Mode: string(p.Mode)})
	}
	payload, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("environment: encoding event payload: %w", err)
	}
	stamped, err := event.New(s.cfg.IDs, s.cfg.Clock, by, event.Event{
		Type:          event.EnvironmentCreated,
		AggregateType: event.AggregateEnvironment,
		AggregateID:   string(e.ID),
		Payload:       payload,
	})
	if err != nil {
		return fmt.Errorf("environment: recording %s: %w", event.EnvironmentCreated, err)
	}
	if _, err := s.cfg.Events.Append(ctx, stamped); err != nil {
		return fmt.Errorf("environment: recording %s: %w", event.EnvironmentCreated, err)
	}
	return nil
}
