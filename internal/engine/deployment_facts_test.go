package engine

import (
	"testing"

	"go.klarlabs.de/rollops/internal/rollout"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// A notification carried only a target ref and a rollout ID: the ref says where
// something was deployed but not what, and the ID is resolvable only by asking us
// back. A consumer recording deployment evidence needs both facts, and a receiver
// should not have to call back to understand what it was told.

func TestDeploymentFactsFromLabels(t *testing.T) {
	r := rollout.Rollout{Desired: pt.Manifest{Labels: map[string]string{
		LabelVersion:     "1.2.0",
		LabelEnvironment: "prod",
	}}}

	version, environment := deploymentFacts(r)
	if version != "1.2.0" {
		t.Errorf("version = %q, want 1.2.0", version)
	}
	if environment != "prod" {
		t.Errorf("environment = %q, want prod", environment)
	}
}

// Version is not first-class here — it lives inside a target-specific spec whose
// shape differs per kind — so an unlabelled rollout reports no version. Empty says
// "unreported", which a consumer can act on; a value guessed from a spec we do not
// model would be a claim we cannot support.
func TestDeploymentFactsWithoutLabels(t *testing.T) {
	version, environment := deploymentFacts(rollout.Rollout{})
	if version != "" || environment != "" {
		t.Errorf("got version=%q environment=%q, want both empty", version, environment)
	}

	// And a manifest with labels but no version label is the same case.
	r := rollout.Rollout{Desired: pt.Manifest{Labels: map[string]string{"team": "platform"}}}
	if version, _ := deploymentFacts(r); version != "" {
		t.Errorf("version = %q, want empty when unlabelled", version)
	}
}
