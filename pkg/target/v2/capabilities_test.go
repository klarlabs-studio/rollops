package targetv2_test

import (
	"reflect"
	"testing"

	targetv2 "go.klarlabs.de/rollops/pkg/target/v2"
)

func TestEveryCapabilityHasAName(t *testing.T) {
	// A field with no name cannot be written in a manifest, so it cannot be
	// authorized, so it is a capability nobody can grant. The struct and the
	// name list are two halves of one contract and this is the seam.
	fields := reflect.TypeOf(targetv2.Capabilities{}).NumField()
	if got := len(targetv2.AllCapabilities()); got != fields {
		t.Fatalf("%d capability names for %d fields", got, fields)
	}
	for _, c := range targetv2.AllCapabilities() {
		var caps targetv2.Capabilities
		if err := caps.Set(c, true); err != nil {
			t.Errorf("Set(%q): %v", c, err)
			continue
		}
		if !caps.Has(c) {
			t.Errorf("setting %q did not make Has(%q) true", c, c)
		}
		if got := caps.Names(); len(got) != 1 || got[0] != c {
			t.Errorf("a target with only %q names %v", c, got)
		}
	}
}

func TestNamesTheHostDoesNotKnowAreIgnored(t *testing.T) {
	// Forward compatibility: a newer plugin may name a capability this host
	// has never heard of. It is not an error, because a ceiling that mentions
	// something unknown caps nothing — the capability simply is not used.
	caps, unknown := targetv2.ParseCapabilities([]string{"drift", "teleportation"})
	if !caps.DriftDetection {
		t.Errorf("drift was not parsed alongside an unknown name")
	}
	if len(unknown) != 1 || unknown[0] != "teleportation" {
		t.Errorf("unknown names are %v, want [teleportation]", unknown)
	}
}

func TestAClaimIsNarrowedToWhatWasInstalled(t *testing.T) {
	// §33.4: installing a plugin is not the same as authorizing it at
	// runtime. The manifest is what an operator agreed to; a runtime claim
	// beyond it is dropped rather than honoured.
	authorized := targetv2.Capabilities{DriftDetection: true}
	claimed := targetv2.Capabilities{DriftDetection: true, NativeRollback: true}

	effective, over := authorized.Narrow(claimed)

	if !effective.DriftDetection {
		t.Errorf("drift was authorized and claimed but is not effective")
	}
	if effective.NativeRollback {
		t.Errorf("rollback was never authorized but is effective")
	}
	if len(over) != 1 || over[0] != targetv2.CapabilityNativeRollback {
		t.Errorf("overclaimed is %v, want [%s]", over, targetv2.CapabilityNativeRollback)
	}
}

func TestClaimingLessThanAuthorizedIsNotAnOverclaim(t *testing.T) {
	// The case the runtime call exists for: a plugin that supports drift in
	// general, bound to an environment where it cannot. Honest, not suspect.
	authorized := targetv2.Capabilities{DriftDetection: true, NativeRollback: true}
	claimed := targetv2.Capabilities{NativeRollback: true}

	effective, over := authorized.Narrow(claimed)

	if effective.DriftDetection {
		t.Errorf("the target said it cannot detect drift here and was overruled")
	}
	if !effective.NativeRollback {
		t.Errorf("rollback was authorized and claimed but is not effective")
	}
	if len(over) != 0 {
		t.Errorf("claiming less than authorized reported an overclaim: %v", over)
	}
}

func TestOverclaimsAreReportedInOneStableOrder(t *testing.T) {
	// The overclaim is a trust signal an operator reads, so it must not
	// reorder itself between two runs of the same plugin.
	claimed := targetv2.Capabilities{
		ProgressiveDelivery: true,
		NativeRollback:      true,
		DriftDetection:      true,
		Prune:               true,
		TrafficSplitting:    true,
		HealthObservation:   true,
	}
	first, over := targetv2.Capabilities{}.Narrow(claimed)
	if first != (targetv2.Capabilities{}) {
		t.Errorf("a manifest authorizing nothing yielded %+v", first)
	}
	if !reflect.DeepEqual(over, targetv2.AllCapabilities()) {
		t.Errorf("overclaimed is %v, want every capability in canonical order %v", over, targetv2.AllCapabilities())
	}
}

func TestSetRejectsANameItCannotHonour(t *testing.T) {
	var caps targetv2.Capabilities
	if err := caps.Set("teleportation", true); err == nil {
		t.Fatalf("Set accepted an unknown capability and silently did nothing")
	}
}
