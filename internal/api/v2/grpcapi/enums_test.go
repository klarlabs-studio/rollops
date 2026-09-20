package grpcapi

// Internal, because what is being checked is the tables rather than the
// behaviour. From outside, a status with no entry renders as UNSPECIFIED and
// looks exactly like a status somebody decided was unspecified — the same
// reason exhaustive_test.go reads grpcStatus directly.
//
// Every enum is held in three directions: the domain publishes a value the
// proto must name, the table names no value the domain does not publish, and
// the proto declares no value the table leaves unreachable. The first catches a
// forgotten addition, the second a rename, the third a proto edited ahead of
// the domain.

import (
	"testing"

	rollopsv2 "go.klarlabs.de/rollops/internal/api/v2/grpcapi/rollopsv2"
	"go.klarlabs.de/rollops/internal/domain/artifact"
	"go.klarlabs.de/rollops/internal/domain/deployment"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/policy"
	verifyv1 "go.klarlabs.de/rollops/pkg/verify/v1"
)

func TestTheProtoEnumsNameExactlyWhatTheDomainPublishes(t *testing.T) {
	t.Parallel()

	holds(t, "deployment status", deploymentStatuses,
		names(deployment.Statuses()), rollopsv2.DeploymentStatus_name)
	holds(t, "strategy", strategies,
		names(deployment.Strategies()), rollopsv2.Strategy_name)
	holds(t, "trigger type", triggerTypes,
		names(deployment.TriggerTypes()), rollopsv2.TriggerType_name)
	holds(t, "environment kind", environmentKinds,
		names(environment.Kinds()), rollopsv2.EnvironmentKind_name)
	holds(t, "policy mode", policyModes,
		names(environment.PolicyModes()), rollopsv2.PolicyMode_name)
	holds(t, "principal type", principalTypes,
		names(identity.PrincipalTypes()), rollopsv2.PrincipalType_name)
	holds(t, "artifact kind", artifactKinds,
		names(artifact.Kinds()), rollopsv2.ArtifactKind_name)
	holds(t, "risk level", riskLevels,
		names(policy.RiskLevels()), rollopsv2.RiskLevel_name)
	holds(t, "requirement type", requirementTypes,
		names(policy.RequirementTypes()), rollopsv2.RequirementType_name)
	holds(t, "verdict", verdicts,
		names(verifyv1.Verdicts()), rollopsv2.Verdict_name)
}

func TestARiskLevelKeepsItsOrderOnTheWire(t *testing.T) {
	t.Parallel()

	// The proto says a client may compare these numbers, so the numbering has to
	// ascend with the risk the domain orders by. Publishing an enum that sorts
	// differently from the thing it names would have a caller gate on "high or
	// above" and let a critical change through.
	levels := policy.RiskLevels()
	for i := 1; i < len(levels); i++ {
		lower, higher := riskLevels.enum(string(levels[i-1])), riskLevels.enum(string(levels[i]))
		if lower >= higher {
			t.Errorf("%s numbers %d and %s numbers %d, so the wire order is not the domain's",
				levels[i-1], lower, levels[i], higher)
		}
	}
}

func TestAVerdictKeepsItsOrderOnTheWire(t *testing.T) {
	t.Parallel()

	// Same argument as risk, and it bites harder: a run's verdict is the most
	// blocking of its checks, so a client that reduces them itself by comparing
	// these numbers must reduce to the same answer.
	all := verifyv1.Verdicts()
	for i := 1; i < len(all); i++ {
		lower, higher := verdicts.enum(string(all[i-1])), verdicts.enum(string(all[i]))
		if lower >= higher {
			t.Errorf("%s numbers %d and %s numbers %d, so the wire order is not the one Combine reduces with",
				all[i-1], lower, all[i], higher)
		}
	}
}

// holds checks one vocabulary against the domain list it publishes and the
// proto enum it is spelled as.
func holds[E ~int32](
	t *testing.T, label string, v vocabulary[E], domain []string, declared map[int32]string,
) {
	t.Helper()

	// Two names sharing one enum value would make the inverse table shorter than
	// the forward one, and one of the two would translate back as the other.
	if len(v.byEnum) != len(v.byName) {
		t.Errorf("%s: %d names share fewer than %d enum values",
			label, len(v.byName), len(v.byName))
	}

	published := make(map[string]bool, len(domain))
	for _, name := range domain {
		published[name] = true
		switch e, mapped := v.byName[name]; {
		case !mapped:
			t.Errorf("%s: the domain publishes %q and the proto does not name it", label, name)
		case e == 0:
			t.Errorf("%s: %q maps to UNSPECIFIED, which reads as no value at all", label, name)
		case v.name(e) != name:
			t.Errorf("%s: %q translates back as %q", label, name, v.name(e))
		}
	}

	for name := range v.byName {
		if !published[name] {
			t.Errorf("%s: the table maps %q, which the domain does not publish", label, name)
		}
	}

	for number, spelling := range declared {
		if number == 0 {
			continue
		}
		if _, reachable := v.byEnum[E(number)]; !reachable {
			t.Errorf("%s: the proto declares %s, which nothing translates to", label, spelling)
		}
	}
}

func names[T ~string](in []T) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, string(v))
	}
	return out
}
