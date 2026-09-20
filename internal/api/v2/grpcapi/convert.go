// Views in, proto messages out — and requests the other way.
//
// Nothing here decides anything. A function that started refusing a value would
// be a rule the HTTP surface does not have, which is the whole thing §23.1 asks
// this layer not to do: the service validates, and a request that carries
// nonsense reaches it as nonsense and is refused once, in the one place that
// can refuse it the same way for every transport.
package grpcapi

import (
	"time"

	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	rollopsv2 "go.klarlabs.de/rollops/internal/api/v2/grpcapi/rollopsv2"
	"go.klarlabs.de/rollops/internal/api/v2/page"
)

// stamp renders a time, leaving an absent one absent. A Timestamp field has
// presence, so a zero time.Time sent as one would read as 1970 rather than as
// "not yet" — which is the distinction Deployment.StartedAt exists to keep.
func stamp(t time.Time) *timestamppb.Timestamp {
	if t.IsZero() {
		return nil
	}
	return timestamppb.New(t)
}

func stampPtr(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return stamp(*t)
}

// span renders a TTL. Zero means the environment does not expire, which is the
// absent duration rather than a duration of nothing.
func span(d time.Duration) *durationpb.Duration {
	if d <= 0 {
		return nil
	}
	return durationpb.New(d)
}

func each[In, Out any](in []In, f func(In) Out) []Out {
	if len(in) == 0 {
		return nil
	}
	out := make([]Out, 0, len(in))
	for _, v := range in {
		out = append(out, f(v))
	}
	return out
}

func pageOf(p *rollopsv2.PageRequest) page.Request {
	return page.Request{Cursor: p.GetPageToken(), Size: int(p.GetPageSize())}
}

func protoPrincipal(p apiv2.Principal) *rollopsv2.Principal {
	return &rollopsv2.Principal{
		Id:          p.ID,
		Type:        principalTypes.enum(p.Type),
		DisplayName: p.DisplayName,
	}
}

func protoProject(p apiv2.Project) *rollopsv2.Project {
	return &rollopsv2.Project{
		Id:          p.ID,
		Name:        p.Name,
		Description: p.Description,
		Labels:      p.Labels,
		CreatedAt:   stamp(p.CreatedAt),
		UpdatedAt:   stamp(p.UpdatedAt),
		Revision:    p.Revision,
	}
}

func protoEnvironment(e apiv2.Environment) *rollopsv2.Environment {
	return &rollopsv2.Environment{
		Id:            e.ID,
		ProjectId:     e.ProjectID,
		Name:          e.Name,
		Kind:          environmentKinds.enum(e.Kind),
		Targets:       each(e.Targets, protoTargetBinding),
		Policies:      each(e.Policies, protoPolicyBinding),
		VariableNames: e.Variables,
		Labels:        e.Labels,
		Lifecycle: &rollopsv2.Lifecycle{
			Ttl:           span(e.Lifecycle.TTL),
			DeleteOnClose: e.Lifecycle.DeleteOnClose,
		},
		Revision: e.Revision,
	}
}

func protoTargetBinding(t apiv2.TargetBinding) *rollopsv2.TargetBinding {
	return &rollopsv2.TargetBinding{
		Name:       t.Name,
		Driver:     t.Driver,
		ConfigKeys: t.Config,
		Labels:     t.Labels,
	}
}

func protoPolicyBinding(p apiv2.PolicyBinding) *rollopsv2.PolicyBinding {
	return &rollopsv2.PolicyBinding{
		Name: p.Name,
		Ref:  p.Ref,
		Mode: policyModes.enum(p.Mode),
	}
}

// apiValue reads a configured value. An unset oneof is an empty literal rather
// than an error: a caller setting a key to nothing meant to set it, and it is
// the service that decides whether nothing is allowed there.
func apiValue(v *rollopsv2.Value) apiv2.Value {
	return apiv2.Value{Literal: v.GetLiteral(), Secret: v.GetSecret()}
}

func apiValues(in map[string]*rollopsv2.Value) map[string]apiv2.Value {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]apiv2.Value, len(in))
	for k, v := range in {
		out[k] = apiValue(v)
	}
	return out
}

func apiTargetSpec(t *rollopsv2.TargetSpec) apiv2.TargetSpec {
	return apiv2.TargetSpec{
		Name:   t.GetName(),
		Driver: t.GetDriver(),
		Config: apiValues(t.GetConfig()),
		Labels: t.GetLabels(),
	}
}

// apiLifecycle reads a lifecycle. An absent TTL is no TTL, which is what a
// zero duration already means to the service.
func apiLifecycle(l *rollopsv2.Lifecycle) apiv2.Lifecycle {
	return apiv2.Lifecycle{
		TTL:           l.GetTtl().AsDuration(),
		DeleteOnClose: l.GetDeleteOnClose(),
	}
}

func apiPolicyBinding(p *rollopsv2.PolicyBinding) apiv2.PolicyBinding {
	return apiv2.PolicyBinding{
		Name: p.GetName(),
		Ref:  p.GetRef(),
		Mode: policyModes.name(p.GetMode()),
	}
}

func protoArtifact(a apiv2.Artifact) *rollopsv2.Artifact {
	return &rollopsv2.Artifact{
		Id:         a.ID,
		ProjectId:  a.ProjectID,
		Kind:       artifactKinds.enum(a.Kind),
		Digest:     a.Digest,
		Locator:    a.Locator,
		Size:       a.Size,
		MediaType:  a.MediaType,
		Metadata:   a.Metadata,
		Provenance: protoDocument(a.Provenance),
		Sboms:      each(a.SBOMs, protoDocument),
		Signatures: each(a.Signatures, protoDocument),
		CreatedAt:  stamp(a.CreatedAt),
	}
}

func protoDocument(d apiv2.DocumentRef) *rollopsv2.DocumentRef {
	// An absent reference stays absent rather than becoming a message of four
	// empty strings, which the service would then try to parse as a digest.
	if d == (apiv2.DocumentRef{}) {
		return nil
	}
	return &rollopsv2.DocumentRef{
		Kind:    d.Kind,
		Format:  d.Format,
		Locator: d.Locator,
		Digest:  d.Digest,
	}
}

func apiDocument(d *rollopsv2.DocumentRef) apiv2.DocumentRef {
	return apiv2.DocumentRef{
		Kind:    d.GetKind(),
		Format:  d.GetFormat(),
		Locator: d.GetLocator(),
		Digest:  d.GetDigest(),
	}
}

func protoRelease(r apiv2.Release) *rollopsv2.Release {
	return &rollopsv2.Release{
		Id:          r.ID,
		ProjectId:   r.ProjectID,
		Version:     r.Version,
		Artifacts:   each(r.Artifacts, protoReleaseArtifact),
		Source:      protoSource(r.Source),
		Provenance:  protoDocument(r.Provenance),
		CreatedBy:   protoPrincipal(r.CreatedBy),
		CreatedAt:   stamp(r.CreatedAt),
		Labels:      r.Labels,
		Annotations: r.Annotations,
	}
}

func protoReleaseArtifact(a apiv2.ReleaseArtifact) *rollopsv2.ReleaseArtifact {
	return &rollopsv2.ReleaseArtifact{Role: a.Role, ArtifactId: a.ArtifactID}
}

func apiReleaseArtifact(a *rollopsv2.ReleaseArtifact) apiv2.ReleaseArtifact {
	return apiv2.ReleaseArtifact{Role: a.GetRole(), ArtifactID: a.GetArtifactId()}
}

func protoSource(s apiv2.Source) *rollopsv2.Source {
	if s == (apiv2.Source{}) {
		return nil
	}
	return &rollopsv2.Source{
		Provider:   s.Provider,
		Repository: s.Repository,
		Revision:   s.Revision,
		Ref:        s.Ref,
		TreeDigest: s.TreeDigest,
		Url:        s.URL,
	}
}

func apiSource(s *rollopsv2.Source) apiv2.Source {
	return apiv2.Source{
		Provider:   s.GetProvider(),
		Repository: s.GetRepository(),
		Revision:   s.GetRevision(),
		Ref:        s.GetRef(),
		TreeDigest: s.GetTreeDigest(),
		URL:        s.GetUrl(),
	}
}

func protoDeployment(d apiv2.Deployment) *rollopsv2.Deployment {
	return &rollopsv2.Deployment{
		Id:            d.ID,
		ProjectId:     d.ProjectID,
		EnvironmentId: d.EnvironmentID,
		ReleaseId:     d.ReleaseID,
		PlanId:        d.PlanID,
		Strategy:      strategies.enum(d.Strategy),
		Status:        deploymentStatuses.enum(d.Status),
		Trigger: &rollopsv2.Trigger{
			Type:   triggerTypes.enum(d.Trigger.Type),
			Detail: d.Trigger.Detail,
		},
		Actor:                protoPrincipal(d.Actor),
		CreatedAt:            stamp(d.CreatedAt),
		StartedAt:            stampPtr(d.StartedAt),
		FinishedAt:           stampPtr(d.FinishedAt),
		PreviousDeploymentId: d.Previous,
		Revision:             d.Revision,
	}
}

func protoPlan(p apiv2.Plan) *rollopsv2.Plan {
	return &rollopsv2.Plan{
		Id:            p.ID,
		ProjectId:     p.ProjectID,
		EnvironmentId: p.EnvironmentID,
		ReleaseId:     p.ReleaseID,
		BaseRevision:  p.BaseRevision,
		Strategy:      strategies.enum(p.Strategy),
		Operations:    each(p.Operations, protoOperation),
		Policy:        protoDecision(p.Policy),
		Rollback: &rollopsv2.RollbackPlan{
			FromRelease: p.Rollback.FromRelease,
			ToRelease:   p.Rollback.ToRelease,
			Operations:  each(p.Rollback.Operations, protoOperation),
			Automatic:   p.Rollback.Automatic,
		},
		CreatedBy: protoPrincipal(p.CreatedBy),
		CreatedAt: stamp(p.CreatedAt),
		ExpiresAt: stamp(p.ExpiresAt),
		Hash:      p.Hash,
	}
}

func protoOperation(o apiv2.PlannedOperation) *rollopsv2.PlannedOperation {
	return &rollopsv2.PlannedOperation{
		Id:           o.ID,
		Target:       o.Target,
		Kind:         o.Kind,
		Summary:      o.Summary,
		Changes:      each(o.Changes, protoChange),
		Dependencies: o.Dependencies,
		Reversible:   o.Reversible,
	}
}

func protoChange(c apiv2.Change) *rollopsv2.Change {
	return &rollopsv2.Change{Path: c.Path, From: c.From, To: c.To, Sensitive: c.Sensitive}
}

func protoDecision(d apiv2.Decision) *rollopsv2.Decision {
	return &rollopsv2.Decision{
		Allowed:      d.Allowed,
		Requirements: each(d.Requirements, protoRequirement),
		Reasons:      each(d.Reasons, protoReason),
		Risk: &rollopsv2.RiskAssessment{
			Level:   riskLevels.enum(d.Risk.Level),
			Score:   d.Risk.Score,
			Factors: each(d.Risk.Factors, protoRiskFactor),
		},
	}
}

func protoRequirement(r apiv2.Requirement) *rollopsv2.Requirement {
	return &rollopsv2.Requirement{
		Type:   requirementTypes.enum(r.Type),
		Role:   r.Role,
		Count:  int32(r.Count),
		Detail: r.Detail,
	}
}

func protoReason(r apiv2.Reason) *rollopsv2.Reason {
	return &rollopsv2.Reason{Code: r.Code, Message: r.Message}
}

func protoRiskFactor(f apiv2.RiskFactor) *rollopsv2.RiskFactor {
	return &rollopsv2.RiskFactor{Code: f.Code, Message: f.Message}
}

func protoVerificationRun(r apiv2.VerificationRun) *rollopsv2.VerificationRun {
	return &rollopsv2.VerificationRun{
		Id:           r.ID,
		DeploymentId: r.DeploymentID,
		PlanId:       r.PlanID,
		Verdict:      verdicts.enum(r.Verdict),
		Checks:       each(r.Checks, protoCheck),
		StartedAt:    stamp(r.StartedAt),
		FinishedAt:   stamp(r.FinishedAt),
		Actor:        protoPrincipal(r.Actor),
	}
}

func protoCheck(c apiv2.Check) *rollopsv2.Check {
	return &rollopsv2.Check{
		Verifier: &rollopsv2.Verifier{
			Kind:    c.Verifier.Kind,
			Name:    c.Verifier.Name,
			Version: c.Verifier.Version,
		},
		Verdict:      verdicts.enum(c.Verdict),
		Measurements: each(c.Measurements, protoMeasurement),
		StartedAt:    stamp(c.StartedAt),
		FinishedAt:   stamp(c.FinishedAt),
		Reason:       c.Reason,
		Evidence:     each(c.Evidence, protoEvidence),
	}
}

func protoMeasurement(m apiv2.Measurement) *rollopsv2.Measurement {
	return &rollopsv2.Measurement{Name: m.Name, Value: m.Value}
}

func protoEvidence(e apiv2.EvidenceRef) *rollopsv2.EvidenceRef {
	return &rollopsv2.EvidenceRef{Kind: e.Kind, Uri: e.URI}
}

func protoEvent(e apiv2.Event) *rollopsv2.Event {
	return &rollopsv2.Event{
		Id:            e.ID,
		Type:          e.Type,
		Version:       int32(e.Version),
		AggregateType: e.AggregateType,
		AggregateId:   e.AggregateID,
		Sequence:      e.Sequence,
		Time:          stamp(e.Time),
		Principal:     protoPrincipal(e.Principal),
		CorrelationId: e.CorrelationID,
		CausationId:   e.CausationID,
		Payload:       e.Payload,
		Metadata:      e.Metadata,
	}
}
