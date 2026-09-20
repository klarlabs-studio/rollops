// The closed sets, as proto enums and back.
//
// A view carries these as strings, because the service layer is shared with
// transports that have no enums to speak of. Proto does have them, and §24 asks
// for them: an enum is checkable by a generated client, where a string is a
// value somebody has to look up in prose.
//
// What makes that safe is the domain's published vocabularies. Each table below
// is held against one — deployment.Statuses(), environment.Kinds() and the rest
// — by a test in this package, in both directions: a domain value with no proto
// counterpart fails, and so does a proto value naming something the domain does
// not have. The failure mode that argument exists to prevent is a new status
// reaching a caller as UNSPECIFIED, which reads as "there is no status" rather
// than "this build is older than that status".
package grpcapi

import rollopsv2 "go.klarlabs.de/rollops/internal/api/v2/grpcapi/rollopsv2"

// vocabulary is one closed set, translatable both ways.
type vocabulary[E comparable] struct {
	byName map[string]E
	byEnum map[E]string
}

// published inverts the table so that neither direction is written out twice.
func published[E comparable](byName map[string]E) vocabulary[E] {
	byEnum := make(map[E]string, len(byName))
	for name, e := range byName {
		byEnum[e] = name
	}
	return vocabulary[E]{byName: byName, byEnum: byEnum}
}

// enum is the proto value for a domain name. A name this build does not know
// comes back as the enum's zero, which is UNSPECIFIED.
func (v vocabulary[E]) enum(name string) E { return v.byName[name] }

// name is the domain value a proto enum stands for. UNSPECIFIED, and anything
// else undeclared, comes back empty — which the service refuses on a request
// rather than guessing at.
func (v vocabulary[E]) name(e E) string { return v.byEnum[e] }

var deploymentStatuses = published(map[string]rollopsv2.DeploymentStatus{
	"planned":           rollopsv2.DeploymentStatus_DEPLOYMENT_STATUS_PLANNED,
	"awaiting_approval": rollopsv2.DeploymentStatus_DEPLOYMENT_STATUS_AWAITING_APPROVAL,
	"queued":            rollopsv2.DeploymentStatus_DEPLOYMENT_STATUS_QUEUED,
	"applying":          rollopsv2.DeploymentStatus_DEPLOYMENT_STATUS_APPLYING,
	"verifying":         rollopsv2.DeploymentStatus_DEPLOYMENT_STATUS_VERIFYING,
	"paused":            rollopsv2.DeploymentStatus_DEPLOYMENT_STATUS_PAUSED,
	"promoting":         rollopsv2.DeploymentStatus_DEPLOYMENT_STATUS_PROMOTING,
	"succeeded":         rollopsv2.DeploymentStatus_DEPLOYMENT_STATUS_SUCCEEDED,
	"failed":            rollopsv2.DeploymentStatus_DEPLOYMENT_STATUS_FAILED,
	"rolling_back":      rollopsv2.DeploymentStatus_DEPLOYMENT_STATUS_ROLLING_BACK,
	"rolled_back":       rollopsv2.DeploymentStatus_DEPLOYMENT_STATUS_ROLLED_BACK,
	"cancelled":         rollopsv2.DeploymentStatus_DEPLOYMENT_STATUS_CANCELLED,
})

var strategies = published(map[string]rollopsv2.Strategy{
	"recreate":   rollopsv2.Strategy_STRATEGY_RECREATE,
	"rolling":    rollopsv2.Strategy_STRATEGY_ROLLING,
	"canary":     rollopsv2.Strategy_STRATEGY_CANARY,
	"blue_green": rollopsv2.Strategy_STRATEGY_BLUE_GREEN,
})

var triggerTypes = published(map[string]rollopsv2.TriggerType{
	"manual":    rollopsv2.TriggerType_TRIGGER_TYPE_MANUAL,
	"api":       rollopsv2.TriggerType_TRIGGER_TYPE_API,
	"git":       rollopsv2.TriggerType_TRIGGER_TYPE_GIT,
	"schedule":  rollopsv2.TriggerType_TRIGGER_TYPE_SCHEDULE,
	"promotion": rollopsv2.TriggerType_TRIGGER_TYPE_PROMOTION,
	"rollback":  rollopsv2.TriggerType_TRIGGER_TYPE_ROLLBACK,
})

var environmentKinds = published(map[string]rollopsv2.EnvironmentKind{
	"development": rollopsv2.EnvironmentKind_ENVIRONMENT_KIND_DEVELOPMENT,
	"preview":     rollopsv2.EnvironmentKind_ENVIRONMENT_KIND_PREVIEW,
	"staging":     rollopsv2.EnvironmentKind_ENVIRONMENT_KIND_STAGING,
	"production":  rollopsv2.EnvironmentKind_ENVIRONMENT_KIND_PRODUCTION,
	"custom":      rollopsv2.EnvironmentKind_ENVIRONMENT_KIND_CUSTOM,
})

var policyModes = published(map[string]rollopsv2.PolicyMode{
	"enforce": rollopsv2.PolicyMode_POLICY_MODE_ENFORCE,
	"warn":    rollopsv2.PolicyMode_POLICY_MODE_WARN,
})

var principalTypes = published(map[string]rollopsv2.PrincipalType{
	"human":     rollopsv2.PrincipalType_PRINCIPAL_TYPE_HUMAN,
	"service":   rollopsv2.PrincipalType_PRINCIPAL_TYPE_SERVICE,
	"agent":     rollopsv2.PrincipalType_PRINCIPAL_TYPE_AGENT,
	"git":       rollopsv2.PrincipalType_PRINCIPAL_TYPE_GIT,
	"scheduler": rollopsv2.PrincipalType_PRINCIPAL_TYPE_SCHEDULER,
	"system":    rollopsv2.PrincipalType_PRINCIPAL_TYPE_SYSTEM,
})

var artifactKinds = published(map[string]rollopsv2.ArtifactKind{
	"oci-image":       rollopsv2.ArtifactKind_ARTIFACT_KIND_OCI_IMAGE,
	"oci-artifact":    rollopsv2.ArtifactKind_ARTIFACT_KIND_OCI_ARTIFACT,
	"binary":          rollopsv2.ArtifactKind_ARTIFACT_KIND_BINARY,
	"archive":         rollopsv2.ArtifactKind_ARTIFACT_KIND_ARCHIVE,
	"helm-chart":      rollopsv2.ArtifactKind_ARTIFACT_KIND_HELM_CHART,
	"manifest-bundle": rollopsv2.ArtifactKind_ARTIFACT_KIND_MANIFEST_BUNDLE,
	"wasm":            rollopsv2.ArtifactKind_ARTIFACT_KIND_WASM,
	"file":            rollopsv2.ArtifactKind_ARTIFACT_KIND_FILE,
})

var riskLevels = published(map[string]rollopsv2.RiskLevel{
	"low":      rollopsv2.RiskLevel_RISK_LEVEL_LOW,
	"medium":   rollopsv2.RiskLevel_RISK_LEVEL_MEDIUM,
	"high":     rollopsv2.RiskLevel_RISK_LEVEL_HIGH,
	"critical": rollopsv2.RiskLevel_RISK_LEVEL_CRITICAL,
})

var requirementTypes = published(map[string]rollopsv2.RequirementType{
	"approval":        rollopsv2.RequirementType_REQUIREMENT_TYPE_APPROVAL,
	"signed_artifact": rollopsv2.RequirementType_REQUIREMENT_TYPE_SIGNED_ARTIFACT,
	"provenance":      rollopsv2.RequirementType_REQUIREMENT_TYPE_PROVENANCE,
	"staging_success": rollopsv2.RequirementType_REQUIREMENT_TYPE_STAGING_SUCCESS,
	"time_window":     rollopsv2.RequirementType_REQUIREMENT_TYPE_TIME_WINDOW,
	"change_ticket":   rollopsv2.RequirementType_REQUIREMENT_TYPE_CHANGE_TICKET,
})

var verdicts = published(map[string]rollopsv2.Verdict{
	"pass":         rollopsv2.Verdict_VERDICT_PASS,
	"inconclusive": rollopsv2.Verdict_VERDICT_INCONCLUSIVE,
	"cancelled":    rollopsv2.Verdict_VERDICT_CANCELLED,
	"error":        rollopsv2.Verdict_VERDICT_ERROR,
	"fail":         rollopsv2.Verdict_VERDICT_FAIL,
})
