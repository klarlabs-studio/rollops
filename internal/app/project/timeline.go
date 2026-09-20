package project

import (
	"context"
	"encoding/json"
	"fmt"

	"go.klarlabs.de/rollops/internal/domain/event"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/project"
)

// projectCreated carries the name and nothing else.
//
// The name a project was opened under is a fact about that moment and stays
// true after a rename, where the current name would not. The description and
// the labels are left out: they are prose and metadata somebody edits, and a
// log nothing may rewrite (INV-013) should not hold a claim that stops being
// true. The project record holds both.
type projectCreated struct {
	Name string `json:"name"`
}

// record files the creation against the project. It starts its own chain: a
// project is not caused by anything this system did, and hanging it off
// whatever happened to be nearby would invent a cause.
func (s *Service) record(ctx context.Context, by identity.Principal, p project.Project) error {
	payload, err := json.Marshal(projectCreated{Name: p.Name})
	if err != nil {
		return fmt.Errorf("project: encoding event payload: %w", err)
	}
	stamped, err := event.New(s.cfg.IDs, s.cfg.Clock, by, event.Event{
		Type:          event.ProjectCreated,
		AggregateType: event.AggregateProject,
		AggregateID:   string(p.ID),
		Payload:       payload,
	})
	if err != nil {
		return fmt.Errorf("project: recording %s: %w", event.ProjectCreated, err)
	}
	if _, err := s.cfg.Events.Append(ctx, stamped); err != nil {
		return fmt.Errorf("project: recording %s: %w", event.ProjectCreated, err)
	}
	return nil
}
