package engine

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.klarlabs.de/rollops/internal/rollout"
)

// FleetMember is the latest rollout for one target in a fleet filter.
type FleetMember struct {
	ID     string
	Target string
	Phase  rollout.Phase
}

// FleetReport aggregates latest-per-target phases for a RolloutSet-style prefix
// (e.g. filter "web" or "web@" → members web@east, web@west).
type FleetReport struct {
	Name     string // display name (filter without trailing @)
	Total    int
	Promoted int
	Active   int
	Degraded int
	Awaiting int
	Members  []FleetMember
}

// FleetStatus returns the latest rollout per matching TargetRef. Filter is a
// set name ("web") or explicit prefix ("web@"). Empty filter is an error.
// Denominator is store-observed targets only (never-deployed clusters are
// invisible until first apply).
func (e *Engine) FleetStatus(ctx context.Context, filter string) (FleetReport, error) {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return FleetReport{}, fmt.Errorf("engine: fleet filter required")
	}
	name := strings.TrimSuffix(filter, "@")
	rs, err := e.store.ListRollouts(ctx, 0)
	if err != nil {
		return FleetReport{}, err
	}
	seen := make(map[string]bool)
	var members []FleetMember
	for _, r := range rs { // newest first
		if seen[r.TargetRef] || !fleetMatch(r.TargetRef, filter) {
			continue
		}
		seen[r.TargetRef] = true
		members = append(members, FleetMember{ID: r.ID, Target: r.TargetRef, Phase: r.Phase})
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Target < members[j].Target })
	rep := FleetReport{Name: name, Total: len(members), Members: members}
	for _, m := range members {
		switch {
		case m.Phase == rollout.PhasePromoted:
			rep.Promoted++
		case m.Phase == rollout.PhaseAwaitingApproval:
			rep.Awaiting++
		case m.Phase.Degraded():
			rep.Degraded++
		case m.Phase.Active():
			rep.Active++
		}
	}
	return rep, nil
}

func fleetMatch(ref, filter string) bool {
	if strings.HasSuffix(filter, "@") {
		return strings.HasPrefix(ref, filter)
	}
	if i := strings.IndexByte(ref, '@'); i >= 0 {
		return ref[:i] == filter
	}
	return ref == filter
}
