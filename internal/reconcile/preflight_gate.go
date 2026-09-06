package reconcile

import "go.klarlabs.de/rollops/internal/config"

// configsForPreflightGate returns the configs whose refusal must refuse the
// whole repository tick. Targets with continueOnFailure: true are excluded —
// their failure must not block siblings (#184 escape hatch). WithoutPreflight
// remains the wholesale disable.
func configsForPreflightGate(cfgs []*config.Config) []*config.Config {
	out := make([]*config.Config, 0, len(cfgs))
	for _, c := range cfgs {
		if c == nil || c.Spec.ContinueOnFailure {
			continue
		}
		out = append(out, c)
	}
	return out
}
