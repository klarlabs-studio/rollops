# RollOps Next — Architecture & Implementation Specification

> **Status:** Proposed architecture / coding-agent source of truth  
> **Audience:** Human maintainers and autonomous coding agents  
> **Language:** Go  
> **Repository baseline:** `github.com/klarlabs-studio/rollops` /
> `go.klarlabs.de/rollops`  
> **Primary objective:** Evolve RollOps from rollout orchestration into
> the best open-source, infrastructure-agnostic software delivery
> control plane without losing its small-core, agent-first character.

────────

## 0. How to Use This Document

This document is intentionally prescriptive. Coding agents should treat
statements using **MUST**, **MUST NOT**, **SHOULD**, and **MAY** as
architectural constraints.

When implementation details are not specified:

1. preserve the invariants in this document;
2. prefer the smallest composable abstraction;
3. preserve backward compatibility unless a migration phase explicitly
permits breaking it;
4. do not introduce infrastructure requirements merely for convenience;
5. keep domain logic independent from CLI, HTTP, gRPC, MCP, Git
providers, databases, and UI;
6. write an ADR before introducing a new architectural primitive that
overlaps an existing one.

A coding agent **MUST NOT** reinterpret the product as a GitHub Actions
clone, Kubernetes-only GitOps controller, hosted CI SaaS, YAML DSL
framework, or AI wrapper.

The north-star abstraction is a **Release moving safely through
environments**.

────────

## 1. Intent

### 1.1 Product intent

RollOps is **the open-source control plane for shipping software**.

It should provide one coherent model for:

```text
Source
  ↓
Pipeline
  ↓
Artifact
  ↓
Release
  ↓
Environment
  ↓
Deployment
  ↓
Verification
  ↓
Promotion / Rollback
```

RollOps **MUST** make the safe deployment path easier than an unsafe one.

The system **MUST** work for both humans and autonomous software agents.
Agents are not a separate product mode. Humans, CI systems, Git
webhooks, schedulers, APIs, and AI agents all invoke the same engine
operations and pass through the same authorization, policy, planning,
audit, and execution machinery.

### 1.2 Category

RollOps is **not merely**:

• a CI runner;
• a Kubernetes GitOps reconciler;
• a deployment script;
• a workflow engine;
• an artifact registry;
• an observability platform;
• an AI DevOps assistant.

It is a **software delivery control plane with optional execution
capabilities**.

### 1.3 Core promise

A user should be able to ask:

> What exactly is running in production, where did it come from, what
> verified it, who or what deployed it, what policy allowed it, and how
> do I safely roll it back?

RollOps **MUST** be able to answer from its own durable model.

### 1.4 Product principles

**P1 — Release-centric**

The primary business object is the immutable Release, not a pipeline
run and not a mutable branch.

**P2 — Plan before mutation**

Every meaningful mutation **MUST** support a deterministic planning phase.

**P3 — Immutable artifacts**

Deployments **MUST** resolve mutable references to immutable artifact
identities before apply.

**P4 — Infrastructure agnostic**

Kubernetes is a first-class target, never a platform requirement.

**P5 — Library first**

The Go engine remains the center. CLI, daemon, API, MCP, Git
integration, and UI are adapters.

**P6 — Local/remote semantic parity**

The same domain operation **MUST** have the same semantics in-process and
through a daemon.

**P7 — Small core, pluggable edges**

Providers and targets extend RollOps. They **MUST NOT** leak
provider-specific types into core domain objects.

**P8 — Verification is part of deployment**

A deployment is not successful merely because an apply call returned
nil.

**P9 — Policy is universal**

Policy **MUST** guard transitions regardless of whether the caller is a
human, CI job, webhook, or agent.

**P10 — Agent-native, not agent-special**

Machine-readable plans, stable IDs, explicit risk, typed errors,
idempotency, explainability, and safe rollback are more important than
chat features.

**P11 — No mandatory central server**

Useful local and one-shot workflows **MUST** remain possible without
`rollopsd`.

**P12 — No mandatory GitOps**

Git may be a source of desired state, but API/CLI-driven delivery
remains first-class.

────────

## 2. Existing Baseline to Preserve

As of the public baseline inspected in September 2026, RollOps already
has:

• a Go engine used in-process;
• `rollops` CLI;
• `rollopsd`;
• HTTP/JSON and gRPC APIs;
• embedded MCP;
• Kubernetes, SSH, FTP, and plugin targets;
• target conformance testing;
• dependency DAG behavior;
• risk gates;
• progressive delivery;
• drift detection and reconciliation;
• rollback;
• artifact verification;
• optional metric analysis;
• strict YAML + CEL;
• SQLite runtime state;
• Bolt-backed audit/events;
• RBAC;
• agent guardrails, kill switch, and attribution;
• Git polling/webhook integration;
• secret-provider abstraction;
• target leases and reconcile leadership.

These are assets. The next architecture **MUST** reuse or migrate them
deliberately rather than duplicate them.

────────

## 3. Scope

### 3.1 RollOps 1.x / "Universal CD"

The first milestone after adopting this architecture focuses on:

• Project
• Environment
• Artifact
• Release
• Deployment
• Deployment Plan
• Target SDK v2
• verification
• promotion
• rollback
• drift/reconciliation
• policy
• provenance
• event timeline
• CLI/API/MCP parity
• migration from existing RolloutConfig

### 3.2 RollOps 2.x / "Software Delivery"

After the CD model is stable:

• Pipeline
• PipelineRun
• Job/Step DAG
• Executor SDK
• local executor
• container executor
• Kubernetes executor
• remote executor protocol
• BuildKit integration
• cache abstraction
• test/report ingestion
• artifact production
• GitHub/GitLab source integrations
• preview environments

### 3.3 Later scale features

Only after semantics are stable:

• distributed runner fleet
• autoscaling executors
• PostgreSQL projection store
• multi-region coordination
• large-scale remote cache
• HA control plane
• enterprise identity integrations
• fleet management

### 3.4 Explicit non-goals

Do **NOT** build these into the core:

• a proprietary programming language;
• a complete shell replacement;
• a container runtime;
• a source-code hosting service;
• a package registry;
• a general observability database;
• a Kubernetes requirement;
• a mandatory Git requirement;
• arbitrary Turing-complete YAML;
• a giant marketplace before plugin contracts stabilize;
• a second execution path specifically for AI agents.

────────

## 4. Domain Model

### 4.1 Aggregate overview

```text
Organization (future / optional in OSS)
└── Project
    ├── SourceRef
    ├── PipelineDefinition
    ├── Artifact*
    ├── Release*
    └── Environment*
        ├── TargetBinding*
        ├── PolicyBinding*
        └── Deployment*
            ├── DeploymentPlan
            ├── VerificationRun*
            └── Rollback
```

The OSS core **MAY** remain single-tenant. Domain IDs **MUST** nevertheless be
globally unique enough to support future multi-tenant storage without
redesign.

### 4.2 ID policy

Use opaque typed IDs.

```go
type ProjectID string
type EnvironmentID string
type ArtifactID string
type ReleaseID string
type DeploymentID string
type PlanID string
type VerificationRunID string
type PipelineRunID string
type ExecutionID string
type EventID string
```

Requirements:

• **MUST NOT** expose database row IDs as public identity.
• **SHOULD** use UUIDv7 or ULID.
• IDs **MUST** be stable across API surfaces.
• Human-readable names are aliases, not identity.

### 4.3 Project

A Project is the durable namespace for one deliverable or cohesive
software system.

```go
type Project struct {
    ID          ProjectID
    Name        string
    Description string
    Labels      map[string]string
    CreatedAt   time.Time
    UpdatedAt   time.Time
}
```

A project **MUST NOT** contain provider-specific runtime handles.

### 4.4 Environment

An Environment is a real domain entity, not a string.

```go
type Environment struct {
    ID          EnvironmentID
    ProjectID   ProjectID
    Name        string
    Kind        EnvironmentKind
    Targets     []TargetBinding
    Policies    []PolicyBinding
    Variables   map[string]ValueRef
    Labels      map[string]string
    Lifecycle   EnvironmentLifecycle
}
```

Suggested kinds:

```go
const (
    EnvironmentDevelopment EnvironmentKind = "development"
    EnvironmentPreview     EnvironmentKind = "preview"
    EnvironmentStaging     EnvironmentKind = "staging"
    EnvironmentProduction  EnvironmentKind = "production"
    EnvironmentCustom      EnvironmentKind = "custom"
)
```

Environment lifecycle **MAY** include preview TTL and cleanup policy.

### 4.5 Artifact

Artifact identity **MUST** be immutable.

```go
type Artifact struct {
    ID          ArtifactID
    ProjectID   ProjectID
    Kind        ArtifactKind
    Digest      Digest
    Locator     string
    Size        int64
    MediaType   string
    Metadata    map[string]string
    Provenance  ProvenanceRef
    SBOMs       []SBOMRef
    Signatures  []SignatureRef
    CreatedAt   time.Time
}
```

Supported initial kinds:

• OCI image
• OCI artifact
• binary
• archive
• Helm chart
• manifest bundle
• WASM module
• generic file

Later:

• Terraform/OpenTofu plan
• serverless bundle

`Locator` is where an artifact can be fetched. `Digest` is its identity.

A release **MUST NOT** rely on mutable tags after resolution.

### 4.6 Release

A Release is immutable after creation except for additive metadata that
does not change content identity.

```go
type Release struct {
    ID          ReleaseID
    ProjectID   ProjectID
    Version     string
    Artifacts   []ReleaseArtifact
    Source      SourceRevision
    Provenance  ProvenanceRef
    CreatedBy   Principal
    CreatedAt   time.Time
    Labels      map[string]string
    Annotations map[string]string
}
```

`ReleaseArtifact` gives artifacts roles:

```go
type ReleaseArtifact struct {
    ArtifactID ArtifactID
    Role       string
}
```

Examples: `app`, `migration`, `frontend`, `chart`.

A Release **MUST** be reproducibly identifiable from its artifact set and
source revision.

### 4.7 Deployment

A Deployment records the attempt to make one Release effective in one
Environment.

```go
type Deployment struct {
    ID             DeploymentID
    ProjectID      ProjectID
    EnvironmentID  EnvironmentID
    ReleaseID      ReleaseID
    PlanID         PlanID
    Strategy       DeploymentStrategy
    Status         DeploymentStatus
    Trigger        Trigger
    Actor          Principal
    StartedAt      *time.Time
    FinishedAt     *time.Time
    Previous       *DeploymentID
}
```

Do not overload Deployment with target-specific state. Target operations
are child records/events.

### 4.8 Deployment status

Canonical states:

```text
planned
awaiting_approval
queued
applying
verifying
paused
promoting
succeeded
failed
rolling_back
rolled_back
cancelled
```

Transitions **MUST** be validated centrally.

Use `statekit` for lifecycle implementation where it remains
appropriate.

### 4.9 Deployment Plan

A plan is immutable and content-addressable or hash-verifiable.

```go
type DeploymentPlan struct {
    ID              PlanID
    ProjectID       ProjectID
    EnvironmentID   EnvironmentID
    ReleaseID       ReleaseID
    BaseRevision    Revision
    Operations      []PlannedOperation
    Diff            Diff
    Risk            RiskAssessment
    PolicyDecision  PolicyDecision
    Verification    VerificationPlan
    Rollback        RollbackPlan
    ExpiresAt       time.Time
    CreatedAt       time.Time
    CreatedBy       Principal
    Hash            string
}
```

Apply **MUST** validate:

• plan exists;
• plan is not expired;
• plan hash is intact;
• relevant desired/current state has not changed;
• caller is authorized;
• policy decision is still valid or is re-evaluated;
• required approvals exist.

If state changed, return a typed `PlanStale` error. Never silently
re-plan during apply.

### 4.10 Principal

Every operation **MUST** have an attributable principal.

```go
type Principal struct {
    ID          string
    Type        PrincipalType
    DisplayName string
    Claims      map[string]string
}
```

Types:

```text
human
service
agent
git
scheduler
system
```

Do not encode authorization decisions into the principal itself.

────────

## 5. Source and Provenance Model

### 5.1 Source revision

```go
type SourceRevision struct {
    Provider   string
    Repository string
    Revision   string
    Ref        string
    TreeDigest string
    URL        string
}
```

Revision **MUST** resolve to immutable source identity, normally a commit
SHA.

### 5.2 Provenance

Core provenance model:

```go
type Provenance struct {
    Source       SourceRevision
    Builder      string
    BuildType    string
    InvocationID string
    Materials    []Material
    Attestations []AttestationRef
}
```

Do not invent proprietary external formats. Integrate:

• SLSA provenance
• Sigstore/cosign
• SPDX
• CycloneDX

The core stores references and normalized metadata.

────────

## 6. Delivery Graph

### 6.1 Purpose

The Delivery Graph is the typed internal representation for dependencies
among delivery operations.

It is **not** "YAML execution."

```go
type Graph struct {
    Nodes map[NodeID]Node
    Edges []Edge
}
```

A node has:

```go
type Node interface {
    ID() NodeID
    Kind() NodeKind
    Dependencies() []NodeID
    Inputs() []ValueRef
    Outputs() []OutputSpec
}
```

### 6.2 Requirements

The graph **MUST**:

• be acyclic;
• validate all references before execution;
• have deterministic topological ordering for equal inputs;
• permit independent nodes to execute concurrently;
• make inputs and outputs explicit;
• support cancellation propagation;
• support retry policy without changing semantic identity;
• support resumability;
• expose machine-readable state.

The graph **MUST NOT** contain arbitrary Go callbacks persisted as workflow
state.

### 6.3 Initial CD graph nodes

Before CI is added, graph nodes can represent:

• resolve artifact
• verify artifact
• policy check
• target plan
• approval
• target apply
• health observation
• verification
• promotion
• rollback

### 6.4 CI graph nodes later

• checkout
• command
• test
• build
• publish
• artifact
• report
• deploy

────────

## 7. Engine Architecture

### 7.1 Layering

```text
cmd/*
  ↓
transport adapters (CLI / HTTP / gRPC / MCP)
  ↓
application services / use cases
  ↓
domain
  ↓
ports
  ↓
adapters (store / targets / executors / providers)
```

Dependencies **MUST** point inward.

### 7.2 Proposed package layout

Migration should converge toward:

```text
cmd/
  rollops/
  rollopsd/

internal/
  app/
    project/
    release/
    deployment/
    pipeline/
    environment/

  domain/
    project/
    artifact/
    release/
    environment/
    deployment/
    pipeline/
    policy/
    verification/
    event/

  engine/
    planner/
    apply/
    scheduler/
    graph/

  target/
    registry/
    k8s/
    ssh/
    ftp/
    plugin/

  executor/               # phase 2
    registry/
    local/
    container/
    kubernetes/
    remote/

  verification/
    http/
    prometheus/
    otel/
    command/
    plugin/

  policy/
    cel/
    approval/

  store/
    sqlite/
    memory/
    postgres/              # later

  eventstore/
    sqlite/                # the only one; ADR-0005, §17.3

  projection/
    deployment/
    release/
    timeline/

  source/
    git/
    github/
    gitlab/

  provenance/
    cosign/
    slsa/
    sbom/

  secrets/
  security/
  api/
  mcp/
  config/
  audit/
  reconcile/
  ui/

pkg/
  target/v2/
  executor/v1/            # phase 2
  verification/v1/
  plugin/
  conformance/
```

Do not perform a giant package move as the first change. Introduce
domain objects and application seams incrementally.

### 7.3 Application service example

```go
type DeploymentService struct {
    plans        PlanRepository
    deployments DeploymentRepository
    releases     ReleaseRepository
    environments EnvironmentRepository
    planner      Planner
    policy       PolicyEngine
    events       EventAppender
    clock        Clock
}

func (s *DeploymentService) Plan(
    ctx context.Context,
    cmd PlanDeploymentCommand,
) (DeploymentPlan, error)

func (s *DeploymentService) Apply(
    ctx context.Context,
    cmd ApplyDeploymentCommand,
) (Deployment, error)
```

Transport handlers **MUST NOT** contain deployment business logic.

────────

## 8. Planning Contract

### 8.1 Universal mutation lifecycle

All significant mutations follow:

```text
Inspect → Plan → Authorize/Policy → Apply → Observe → Verify → Record
```

### 8.2 Planner interface

```go
type Planner interface {
    PlanDeployment(
        context.Context,
        PlanDeploymentRequest,
    ) (DeploymentPlan, error)
}
```

Planning **MUST** be side-effect free except:

• reading external state;
• caching read-only discovery;
• persisting the resulting plan;
• emitting audit/event records that a plan was created.

Planning **MUST NOT** mutate deployment targets.

### 8.3 Planned operation

```go
type PlannedOperation struct {
    ID          OperationID
    Target      TargetRef
    Kind        OperationKind
    Summary     string
    Diff        Diff
    Dependencies []OperationID
    RiskHints   []RiskHint
    Reversible  bool
}
```

Plans **SHOULD** be understandable without parsing opaque provider blobs.

Provider-specific payload **MAY** be attached as a versioned opaque field,
but the core summary/diff **MUST** remain portable.

────────

## 9. Target SDK v2

### 9.1 Intent

Targets answer:

> How do I inspect, plan, apply, observe, and roll back desired software
> state on this deployment substrate?

Target SDK v2 **MUST** be deployment-oriented and capability-driven.

### 9.2 Public contract

Illustrative interface:

```go
package target

type Target interface {
    Metadata() Metadata
    Capabilities(context.Context) (Capabilities, error)

    Inspect(
        context.Context,
        InspectRequest,
    ) (ObservedState, error)

    Plan(
        context.Context,
        PlanRequest,
    ) (PlanResult, error)

    Apply(
        context.Context,
        ApplyRequest,
    ) (ApplyResult, error)

    Observe(
        context.Context,
        ObserveRequest,
    ) (Observation, error)

    Rollback(
        context.Context,
        RollbackRequest,
    ) (RollbackResult, error)
}
```

Optional capabilities **SHOULD** use subinterfaces rather than ever-growing
mandatory methods:

```go
type Promoter interface {
    Promote(context.Context, PromoteRequest) (PromoteResult, error)
}

type Drifter interface {
    DetectDrift(context.Context, DriftRequest) (DriftResult, error)
}

type Pruner interface {
    Prune(context.Context, PruneRequest) (PruneResult, error)
}
```

### 9.3 Capability declaration

```go
type Capabilities struct {
    ProgressiveDelivery bool
    NativeRollback       bool
    DriftDetection       bool
    Prune                bool
    TrafficSplitting     bool
    HealthObservation    bool
}
```

Never infer critical capabilities by target name.

### 9.4 Idempotency

Apply **MUST** accept an idempotency key.

Repeated calls with the same key **MUST** either:

• return the same semantic result; or
• return a typed conflict if the provider cannot guarantee replay.

### 9.5 Target conformance

Every first-party and external target **MUST** pass shared conformance.

Conformance **MUST** cover:

• capability truthfulness;
• inspect stability;
• side-effect-free plan;
• idempotent apply semantics;
• cancellation;
• timeout behavior;
• secret redaction;
• typed error mapping;
• rollback behavior when declared;
• drift behavior when declared.

### 9.6 v1 compatibility

Provide an adapter:

```text
Target v1 → targetv1adapter → Target v2
```

Do not force all target implementations to migrate in one release.

Deprecation sequence:

1. introduce v2 alongside v1;
2. first-party targets implement v2;
3. conformance supports both;
4. plugin manifest advertises contract version;
5. warn on v1;
6. remove v1 only at a major-version boundary.

────────

## 10. Deployment Strategies

Core strategy interface:

```go
type Strategy interface {
    Name() string
    BuildPlan(context.Context, StrategyInput) (StrategyPlan, error)
}
```

Initial strategies:

• rolling
• recreate
• canary
• blue/green
• direct

Strategies orchestrate target capabilities; they do not implement
provider APIs.

Example canary:

```text
apply 5%
→ observe
→ verify
→ promote 25%
→ observe
→ verify
→ promote 50%
→ observe
→ verify
→ promote 100%
→ final verify
```

A strategy **MUST** declare rollback behavior and pause semantics.

────────

## 11. Verification

### 11.1 Verification is a first-class subsystem

```go
type Verifier interface {
    Metadata() VerifierMetadata

    Verify(
        context.Context,
        VerificationRequest,
    ) (VerificationResult, error)
}
```

### 11.2 Initial providers

• HTTP probe
• command/test
• Prometheus
• generic webhook/plugin

Then:

• OpenTelemetry
• Grafana
• Datadog
• CloudWatch
• log-query providers
• synthetic providers

### 11.3 Result model

```go
type VerificationResult struct {
    Verdict     Verdict
    Measurements []Measurement
    StartedAt   time.Time
    FinishedAt  time.Time
    Reason      string
    Evidence    []EvidenceRef
}
```

Verdicts:

```text
pass
fail
inconclusive
error
cancelled
```

`inconclusive` **MUST NOT** silently become `pass`.

Policy/config decides whether `inconclusive` blocks promotion.

### 11.4 Verification policy

Example:

```yaml
verification:
  mode: all
  checks:
    - uses: http
      with:
        url: https://api.example.com/health
        expectStatus: 200

    - uses: prometheus
      with:
        query: rate(http_requests_total{status=~"5.."}[5m])
        condition: value < 0.01
        window: 10m

  onFailure: rollback
  onInconclusive: pause
```

────────

## 12. Policy Engine

### 12.1 Policy decision points

Policy **MUST** be invokable at:

```text
project.modify
environment.modify
pipeline.run
artifact.register
release.create
deployment.plan
deployment.apply
deployment.promote
deployment.rollback
secret.access
target.modify
```

### 12.2 Contract

```go
type PolicyEngine interface {
    Evaluate(
        context.Context,
        PolicyInput,
    ) (PolicyDecision, error)
}
```

```go
type PolicyDecision struct {
    Allowed      bool
    Requirements []Requirement
    Reasons      []Reason
    Risk         RiskAssessment
}
```

Requirements can include:

• human approval;
• N approvals;
• specific role approval;
• signed artifact;
• provenance;
• successful staging deployment;
• allowed time window;
• change-ticket reference.

### 12.3 CEL

Keep CEL as a first-class policy expression mechanism.

Do not put all policy semantics directly into YAML parsing.

### 12.4 Risk

Existing blast-radius risk becomes an **input to policy**, not a parallel
authorization system.

Risk assessment **SHOULD** include explainable factors.

```go
type RiskAssessment struct {
    Level   RiskLevel
    Score   float64
    Factors []RiskFactor
}
```

Never let an opaque ML score become the sole authorization mechanism.

────────

## 13. Approvals

Approvals are durable domain records.

```go
type Approval struct {
    ID         ApprovalID
    Subject    SubjectRef
    Principal  Principal
    Decision   ApprovalDecision
    Reason     string
    CreatedAt  time.Time
    ExpiresAt  *time.Time
}
```

An approval **MUST** bind to the exact plan/revision it approves.

Changing the plan invalidates plan-bound approvals unless policy
explicitly defines otherwise.

────────

## 14. Rollback

### 14.1 Definition

Rollback is a new controlled deployment transition, not an ad-hoc
inverse command.

The preferred rollback target is the last known-good immutable Release.

### 14.2 Rollback plan

```go
type RollbackPlan struct {
    FromRelease ReleaseID
    ToRelease   ReleaseID
    Operations  []PlannedOperation
    Automatic   bool
}
```

Rollback **MUST** itself be:

• attributable;
• policy-aware;
• evented;
• observable;
• verifiable.

Emergency policy **MAY** reduce approval requirements but **MUST NOT** bypass
audit.

────────

## 15. Drift and Reconciliation

### 15.1 Separation of concepts

Deployment is an intentional change.

Reconciliation restores declared desired state.

Do not represent every reconcile loop iteration as a new user
deployment.

### 15.2 Reconcile modes

```text
off
detect
alert
reconcile
```

### 15.3 Drift event

```go
type DriftDetected struct {
    EnvironmentID EnvironmentID
    Target        TargetRef
    Expected      Revision
    Observed      Revision
    Diff          Diff
}
```

Reconcile mutations **MUST** still pass policy appropriate for system
principals.

────────

## 16. Event Model

### 16.1 Direction

Converge toward an append-only domain event stream with projections.

This does not require full event sourcing on day one.

### 16.2 Event envelope

```go
type Event struct {
    ID            EventID
    Type          Type
    Version       int
    AggregateType AggregateType
    AggregateID   string
    Sequence      uint64
    Time          time.Time
    Principal     Principal
    CorrelationID EventID
    CausationID   EventID
    Payload       json.RawMessage
    Metadata      map[string]string
}
```

`Type` and `AggregateType` are named string types, not enums: §16.4 requires
consumers to tolerate an unknown type, so one has to be representable. Writing
is the strict direction — the constructor refuses a type this binary does not
know, and refuses an `AggregateID` whose prefix disagrees with
`AggregateType`, because an event filed against the wrong kind is invisible on
the timeline that should show it.

`CorrelationID` and `CausationID` are `EventID` rather than free-form strings.
Both point into this log, so anything else is a reference the timeline cannot
resolve. An event that names no correlation starts its own chain — its
correlation is its own ID — which is how §16.4's "MUST" survives contact with
a field that would otherwise be left empty. An empty `CausationID` means a
root.

`Sequence` is assigned by the appender and by nothing else (ADR-0003): a
constructor that accepted one would let a caller claim a position in a log it
has not been written to.

### 16.3 Initial event vocabulary

```text
project.created
environment.created
artifact.registered
release.created

deployment.plan.created
deployment.queued
deployment.approval.requested
deployment.approved
deployment.approval.rejected
deployment.started
deployment.operation.started
deployment.operation.completed
deployment.operation.failed
deployment.verification.started
deployment.verification.completed
deployment.paused
deployment.promotion.started
deployment.promoted
deployment.succeeded
deployment.failed
deployment.cancelled

rollback.started
rollback.completed
rollback.failed

drift.detected
reconcile.started
reconcile.completed
reconcile.failed

policy.evaluated
security.access_denied
```

`deployment.queued` is not in the original list and was added once apply and
execution were separated. §42.3 describes them as adjacent steps, but apply
admits a deployment and stops: what carries it through is the engine, which may
pick it up much later. Without this event a deployment has no timeline at all
between being admitted and being started, so an operator asking what happened
to their apply sees nothing — and the one question they are asking is whether
it went through.

`deployment.cancelled` was added for the same reason and is subject to one
restriction. §23.2 gives an operator a `:cancel` command, and a deployment that
reaches the `cancelled` status with nothing on its timeline saying who stopped
it and why leaves the next person to look at it unable to tell a deliberate
stop from a crash. The restriction is that a cancellation which is somebody's
answer does not get this event: a deployment stopped by a denied approval
already carries `deployment.approval.rejected`, and recording both would put
two causes on the timeline for one act. This event means an operator
intervened, not merely that the status is `cancelled`.

Pipeline phase later:

```text
pipeline.run.started
job.started
job.completed
artifact.produced
pipeline.run.completed
```

### 16.4 Event requirements

• Events are immutable.
• Event schemas are versioned.
• Consumers **MUST** tolerate unknown event types.
• Secrets **MUST** be redacted before persistence.
• Correlation IDs **MUST** connect one user intent across
plan/apply/verify.
• Causation IDs **MUST** permit causal timeline reconstruction.

### 16.5 Projection model

Use projections for:

• current deployment state;
• environment current release;
• release history;
• timeline;
• risk history;
• dashboard summaries.

A projection can be rebuilt from retained events where practical.

────────

## 17. Persistence

### 17.1 Storage ports

Avoid one giant `Store` interface.

Prefer focused repositories:

```go
type ReleaseRepository interface { ... }
type EnvironmentRepository interface { ... }
type DeploymentRepository interface { ... }
type PlanRepository interface { ... }
type EventAppender interface { ... }
type EventReader interface { ... }
type ApprovalRepository interface { ... }
```

### 17.2 SQLite

SQLite remains the default OSS store.

Requirements:

• zero external database required;
• WAL mode where appropriate;
• migrations are explicit and tested;
• transactional writes for aggregate state + outbox/event append where
required;
• database corruption/error paths return typed errors.

### 17.3 Bolt migration — withdrawn

This section assumed an embedded key-value store holding audit history, to be
dual-read during a compatibility period. There is no such store, and there
never was: `go.klarlabs.de/bolt` is a structured *logging* library, not
`go.etcd.io/bbolt`, and `internal/audit` writes JSON lines to an `io.Writer`
that defaults to `io.Discard`. It has no reader and no file format.

There is therefore nothing to migrate. Domain events are written to SQLite
from empty (ADR-0005), and `eventstore/bolt/` in §17.1 is not created.

The requirement the section was protecting still holds and is restated here:
**durable history MUST NOT be silently discarded.** It binds the event log
from the moment it exists.

### 17.4 PostgreSQL

PostgreSQL **MAY** be added later behind the same ports for
HA/multi-instance scale.

Core semantics **MUST NOT** depend on PostgreSQL-specific behavior.

────────

## 18. Consistency and Concurrency

### 18.1 Optimistic concurrency

Aggregates **SHOULD** use revision numbers.

```go
type Revision uint64
```

Mutating commands include expected revision when relevant.

### 18.2 Environment deployment lock

Only one conflicting mutation per environment/target scope may execute
at once unless the strategy explicitly supports concurrency.

Existing target lease machinery should evolve into this model.

### 18.3 Idempotency

External mutation entry points **MUST** support idempotency keys:

• HTTP header;
• gRPC field/metadata;
• MCP tool argument generated or supplied by caller;
• CLI generated per invocation unless user supplies one.

────────

## 19. Pipeline and CI Architecture — Phase 2

### 19.1 Principle

RollOps orchestrates execution. Executors perform work.

Do not make the deployment target abstraction also execute CI jobs.

### 19.2 Pipeline

```go
type Pipeline struct {
    ID        PipelineID
    ProjectID ProjectID
    Name      string
    Graph     Graph
    Triggers  []PipelineTrigger
}
```

### 19.3 PipelineRun

```go
type PipelineRun struct {
    ID         PipelineRunID
    PipelineID PipelineID
    Source     SourceRevision
    Status     RunStatus
    Trigger    Trigger
    Actor      Principal
}
```

### 19.4 Executor SDK

```go
type Executor interface {
    Metadata() ExecutorMetadata
    Capabilities(context.Context) (ExecutorCapabilities, error)

    Submit(context.Context, Execution) (ExecutionHandle, error)
    Status(context.Context, ExecutionHandle) (ExecutionStatus, error)
    Logs(context.Context, ExecutionHandle, LogOptions) (LogStream, error)
    Cancel(context.Context, ExecutionHandle) error
}
```

### 19.5 Execution

```go
type Execution struct {
    ID          ExecutionID
    Image       string
    Command     []string
    Env         map[string]ValueRef
    WorkingDir  string
    Inputs      []ExecutionInput
    Outputs     []ExecutionOutput
    Resources   ResourceRequirements
    Timeout     time.Duration
    Network     NetworkPolicy
    Security    ExecutionSecurity
}
```

### 19.6 First executors

Order:

1. local
2. container
3. kubernetes
4. remote

SSH **MAY** be an executor later, but do not conflate SSH deployment target
semantics with remote CI execution.

### 19.7 Remote executor protocol

The control plane **MUST** treat remote workers as untrusted execution
infrastructure.

Protocol requirements:

• worker registration;
• capability advertisement;
• lease-based job claim;
• heartbeat;
• cancellation;
• log streaming;
• artifact upload references;
• short-lived credentials;
• no long-lived control-plane secrets on workers;
• replay/idempotency handling.

────────

## 20. Caching — Phase 2

Define cache as a port:

```go
type Cache interface {
    Get(context.Context, CacheKey) (CacheEntry, error)
    Put(context.Context, CacheKey, CacheEntry) error
}
```

Do not make cache correctness affect pipeline correctness.

Cache misses and cache service outages **SHOULD** degrade to execution, not
corrupt results.

Cache keys **MUST** include all semantically relevant inputs.

────────

## 21. Artifact Production — Phase 2

Build jobs produce artifacts explicitly.

Example:

```yaml
jobs:
  build:
    run: docker buildx build ...
    outputs:
      image:
        type: oci
```

The engine registers the immutable artifact and attaches it to a
Release.

Avoid magic string parsing of logs to discover artifacts.

────────

## 22. Configuration Model

### 22.1 Configuration is an input format

YAML is not the domain model.

Parse:

```text
YAML → versioned config AST → validation → domain commands/definitions
```

### 22.2 Versioning

Use explicit API version:

```yaml
apiVersion: rollops.dev/v1alpha1
kind: Project
```

Do not rely on heuristic schema detection.

### 22.3 Suggested unified example

```yaml
apiVersion: rollops.dev/v1alpha1
kind: Project

metadata:
  name: api

source:
  git:
    repository: https://github.com/acme/api
    branch: main

environments:
  staging:
    targets:
      - ref: k8s-staging

  production:
    targets:
      - ref: k8s-production

    policy:
      require:
        signedArtifacts: true
        provenance: true
        successfulEnvironment: staging

    deployment:
      strategy:
        canary:
          steps: [5, 25, 50, 100]

      verification:
        checks:
          - uses: prometheus
            with:
              query: ...
              condition: value < 0.01

        onFailure: rollback

pipeline:
  on:
    push:
      branches: [main]

  jobs:
    test:
      run: go test ./...

    build:
      needs: [test]
      run: ...
```

This is illustrative. Do not freeze syntax before the domain/API types
stabilize.

### 22.4 Expressions

CEL remains the preferred bounded expression language.

Do not add a second expression engine without ADR.

### 22.5 Secrets

Secret values **MUST NOT** appear in normalized config, plans, events, logs,
or API responses.

Use references:

```yaml
env:
  DATABASE_URL:
    secretRef: prod/database-url
```

────────

## 23. API Design

### 23.1 Rule

Define service semantics first, then expose consistently through:

• Go API;
• gRPC;
• HTTP/JSON;
• MCP;
• CLI.

### 23.2 Resource endpoints

Illustrative HTTP:

```text
POST /v2/projects
GET  /v2/projects/{id}

POST /v2/projects/{id}/artifacts
GET  /v2/projects/{id}/artifacts

POST /v2/projects/{id}/releases
GET  /v2/projects/{id}/releases

POST /v2/projects/{id}/environments
GET  /v2/projects/{id}/environments
GET  /v2/environments/{id}

POST /v2/environments/{id}/deployments:plan
POST /v2/deployment-plans/{id}:apply

GET  /v2/deployments/{id}
POST /v2/deployments/{id}:verify
POST /v2/deployments/{id}:promote
POST /v2/deployments/{id}:rollback
POST /v2/deployments/{id}:cancel

GET  /v2/deployments/{id}/events
```

Artifact registration is the one mutation that takes no idempotency key.
Content identifies an artifact (INV-002), so the digest already is one: a build
that reruns registers the same bytes and is answered with the artifact already
recorded, which is what a key would have bought without a row to expire or a
fingerprint to mismatch. Release creation does take one — a version is unique
within its project, so without a key a retry is indistinguishable from a second
attempt to use the name and would come back `CONFLICT`. Project creation takes
one for the same reason, a project name being unique across the estate, and so
does environment creation, a name being unique within its project.

An environment is the one resource whose write and read shapes deliberately
differ. Creating one supplies target configuration and variables as values —
each either an inline literal or the name of a secret the provider holds, never
both, because a value that is both has no reading that is not a guess. Reading
one back returns the **keys** and never the values (INV-012), so the two are
separate types rather than one pressed into both jobs. A read that carried a
literal through would be a read that a caller could not tell from one carrying a
resolved secret.

A key's fingerprint covers what identifies the resource and not what can be
edited afterwards: a project's name but not its description or labels, a
release's version and artifacts but not its labels or annotations. A client
retrying with the prose corrected is retrying, not asking for something else,
and refusing it would leave them unable to retry at all.

### 23.3 Command responses

Mutation commands **SHOULD** return operation/deployment identity
immediately where execution is asynchronous.

Do not hold HTTP requests open for entire deployments.

### 23.4 Errors

Use stable typed error codes:

```text
INVALID_ARGUMENT
NOT_FOUND
CONFLICT
PLAN_STALE
POLICY_DENIED
APPROVAL_REQUIRED
UNAUTHORIZED
FORBIDDEN
TARGET_UNAVAILABLE
CAPABILITY_UNSUPPORTED
VERIFICATION_FAILED
EXECUTION_FAILED
CANCELLED
DEADLINE_EXCEEDED
INTERNAL
```

Transport layers map domain errors without losing codes.

────────

## 24. gRPC / Protobuf

Proto definitions are public contracts.

Rules:

• never reuse removed field numbers;
• use explicit enums;
• use timestamps/durations from well-known types;
• avoid exposing internal database schemas;
• use pagination from the start on list APIs;
• include idempotency keys on mutation requests;
• include resource revision where optimistic concurrency applies.

Prefer a v2 package rather than mutating incompatible v1 messages.

────────

## 25. MCP Design

### 25.1 Intent

MCP mirrors safe engine operations. It is not a privileged bypass.

Initial tools:

```text
rollops.inspect_environment
rollops.list_releases
rollops.create_release
rollops.plan_deployment
rollops.explain_plan
rollops.apply_plan
rollops.get_deployment
rollops.verify_deployment
rollops.promote_deployment
rollops.rollback_deployment
rollops.cancel_deployment
rollops.get_timeline
```

### 25.2 Agent safety

Every tool result **SHOULD** include:

• stable resource IDs;
• current state;
• allowed next actions;
• policy result;
• risk;
• whether approval is required;
• typed failure reason.

Example conceptual response:

```json
{
  "plan_id": "pln_...",
  "risk": {
    "level": "medium",
    "factors": ["production", "3 replicas changed"]
  },
  "policy": {
    "allowed": false,
    "requirements": ["production-approval"]
  },
  "next_actions": [
    "request_approval"
  ]
}
```

Agents **MUST NOT** receive secret values merely because they use MCP.

────────

## 26. CLI UX

The CLI is a primary product surface.

### 26.1 Commands

Target shape:

```text
rollops init
rollops doctor

rollops project ...
rollops environment ...
rollops release ...

rollops plan [environment]
rollops deploy [environment]
rollops status [deployment]
rollops verify [deployment]
rollops promote [deployment]
rollops rollback [deployment|environment]
rollops history [environment]
rollops diff [environment]

rollops run                  # phase 2
rollops logs                 # phase 2
```

### 26.2 Human output

Default output is concise and decision-oriented.

### 26.3 Machine output

All read/plan commands **MUST** support stable JSON output.

```bash
rollops plan production --output json
```

Machine output **MUST NOT** contain terminal formatting.

### 26.4 Exit codes

Use meaningful documented exit codes for:

• success;
• validation error;
• policy block;
• approval required;
• verification failure;
• deployment failure;
• connection/system failure.

Do not force agents to parse English output.

────────

## 27. Security

### 27.1 Default stance

• least privilege;
• explicit principals;
• immutable audit;
• secret redaction;
• plan before mutation;
• fail closed for authorization/policy infrastructure where safety
requires it.

### 27.2 RBAC

Permissions should align with domain actions:

```text
project.read
project.write
environment.read
environment.write
release.read
release.create
deployment.plan
deployment.apply
deployment.promote
deployment.rollback
deployment.cancel
approval.grant
policy.read
policy.write
secret.use
target.read
target.write
```

### 27.3 Agent guardrails

Existing guardrails remain applicable but become policy inputs.

Agent identity **MUST** be explicit.

No `if agent { weakerAuth() }` paths.

### 27.4 Kill switch

The existing kill switch should stop new mutations and optionally cancel
active mutations according to configured policy.

Read-only inspection **SHOULD** remain available.

────────

## 28. Observability

RollOps **MUST** instrument itself using OpenTelemetry-compatible semantics.

### 28.1 Traces

Trace:

```text
request
→ plan
→ policy
→ target inspect
→ target apply
→ verify
→ promote
```

Correlation ID should connect trace and event timeline.

### 28.2 Metrics

Initial metrics:

• deployment duration;
• deployment success/failure;
• rollback count;
• verification failure;
• policy denial;
• plan-to-apply latency;
• target operation latency;
• reconcile drift count;
• active deployments;
• queue depth;
• executor utilization later.

Avoid high-cardinality project/deployment IDs in metrics labels.

### 28.3 Logs

Structured logs only in daemon internals.

Every log **SHOULD** carry:

• correlation ID;
• deployment ID when applicable;
• target reference when applicable;
• principal ID/type where safe.

Never log secrets.

────────

## 29. Reproducibility and Determinism

Plans generated from equivalent inputs **SHOULD** be stable.

The following **MUST NOT** affect plan semantics:

• map iteration order;
• wall-clock time except explicit timestamps/expiry;
• random operation ordering;
• transport used;
• UI/CLI formatting.

Inject clocks and ID generators into application services for
deterministic tests.

────────

## 30. Testing Strategy

### 30.1 Test pyramid

**Domain tests**

Fast table/property tests for:

• state transitions;
• policy composition;
• graph validation;
• plan stale logic;
• release immutability;
• rollback selection;
• strategy behavior.

**Contract tests**

For:

• target SDK;
• executor SDK later;
• verifier SDK;
• repositories.

**Integration tests**

Real:

• SQLite;
• Kubernetes where available;
• SSH;
• FTP;
• cosign;
• Prometheus;
• plugin process boundary.

**End-to-end tests**

At minimum:

```text
config
→ release
→ plan
→ approval
→ apply
→ verify
→ success
```

and:

```text
release
→ canary
→ failed verification
→ automatic rollback
→ previous release healthy
```

and:

```text
agent principal
→ plan
→ policy requires approval
→ apply rejected
→ human approval
→ same plan applied
```

### 30.2 Golden tests

Use golden files for:

• plan JSON;
• diffs;
• normalized config;
• CLI machine output;
• event payload compatibility.

Do not overuse golden tests for human text.

### 30.3 Property tests

Useful invariants:

• graph topological sort respects every edge;
• apply never runs for stale plan;
• release content never mutates;
• secret values never appear in serialized public structures;
• successful rollback targets an existing immutable release;
• event sequence per aggregate is monotonic.

### 30.4 Race tests

`go test -race ./...` **SHOULD** be part of release gates.

────────

## 31. Failure Semantics

Every external/provider error **MUST** be normalized into a typed domain
error with:

• code;
• operation;
• target/provider;
• retryability;
• user-safe message;
• wrapped cause internally.

```go
type OpError struct {
    Code      ErrorCode
    Operation string
    Resource  string
    Retryable bool
    Err       error
}
```

Do not classify every provider error as retryable.

Retry policy belongs at operation orchestration, not scattered through
target implementations.

Existing resilience tooling can remain underneath this contract.

────────

## 32. Cancellation and Timeouts

Every provider operation accepts `context.Context`.

Requirements:

• cancellation propagates to target/executor where supported;
• timeout is explicit;
• cancellation is recorded as an event;
• partial apply is surfaced, never hidden;
• cancellation does not imply rollback unless strategy/policy says so.

────────

## 33. Plugin Architecture

### 33.1 Plugin kinds

Eventually:

```text
target
executor
verifier
secret-provider
source-provider
notification
policy-extension (carefully)
```

### 33.2 Manifest

```yaml
apiVersion: rollops.dev/plugin/v1
kind: Plugin

metadata:
  name: example

spec:
  type: target
  protocolVersion: 2
  executable: ./rollops-target-example
  capabilities:
    - drift
    - rollback
```

### 33.3 Protocol

Out-of-process plugins **MUST** use a versioned RPC protocol.

Do not use Go's native `plugin` package as the primary ecosystem
contract.

### 33.4 Trust

Plugins are executable code.

RollOps **SHOULD** expose:

• plugin origin;
• checksum/signature;
• requested capabilities;
• configured permissions.

Plugin installation is not equivalent to runtime authorization.

────────

## 34. UI Direction

The UI is a projection of the same API, not a separate orchestration
implementation.

Primary screens:

1. Projects
2. Environment overview
3. Release history
4. Deployment detail/timeline
5. Plan/diff/approval
6. Drift
7. Policy
8. Targets
9. Pipeline runs later

The deployment timeline is the signature UI:

```text
10:41 release created from abc123
10:42 plan created
10:42 policy evaluated — approval required
10:44 approved by alice
10:44 deployment started
10:45 canary 5%
10:48 verification passed
10:48 promoted 25%
10:52 verification failed
10:52 rollback started
10:53 previous release restored
10:54 rollback verification passed
```

The UI **MUST NOT** invent state not represented in domain/events.

────────

## 35. Backward Compatibility: RolloutConfig Migration

### 35.1 Principle

Existing `RolloutConfig` remains supported during transition.

### 35.2 Mapping

Conceptually:

```text
RolloutConfig service
→ Project

RolloutConfig target
→ Environment + TargetBinding

artifact/image reference
→ Artifact + Release

plan
→ DeploymentPlan

apply
→ Deployment

verify
→ VerificationRun

promote
→ Deployment transition

rollback
→ Rollback Deployment

reconcile
→ Environment reconciliation
```

### 35.3 Compatibility adapter

Implement:

```go
func TranslateRolloutConfig(
    cfg legacy.RolloutConfig,
) (MigrationBundle, error)
```

`MigrationBundle` contains domain commands/definitions.

The old engine entry points **SHOULD** call through the new application
layer once parity exists.

### 35.4 No flag day

Migration order:

1. introduce new domain types;
2. create repositories/tables;
3. add legacy translator;
4. run old CLI through translator/new engine behind feature flag;
5. compare plan output in parity tests;
6. switch default;
7. keep legacy syntax;
8. add migration command;
9. deprecate old schema only after documented support window.

### 35.5 Migration command

Eventually:

```bash
rollops migrate config rollops.yaml
```

Output new config to stdout by default; never overwrite without explicit
flag.

────────

## 36. Database Migration Plan

Suggested new tables:

```text
projects
environments
target_bindings
artifacts
releases
release_artifacts
deployment_plans
deployments
deployment_operations
verification_runs
approvals
events
projection_offsets
idempotency_keys
```

Phase 2:

```text
pipelines
pipeline_runs
jobs
executions
execution_attempts
cache_records
```

Every migration **MUST** have:

• up migration;
• rollback strategy where feasible;
• upgrade test from latest released schema;
• backup guidance for destructive changes.

────────

## 37. Implementation Phases

### Phase A — Domain foundation

Deliver:

• typed IDs;
• Project;
• Environment;
• Artifact;
• Release;
• Deployment;
• DeploymentPlan;
• Principal;
• domain errors;
• repositories;
• SQLite schema.

Acceptance:

• no existing behavior broken;
• domain packages have no transport/provider imports;
• full unit tests.

### Phase B — Event spine

Deliver:

• event envelope;
• appender/reader;
• deployment/release events;
• correlation/causation;
• timeline projection;
• audit compatibility.

Acceptance:

• every new deployment operation emits events;
• timeline reconstructs a deployment;
• secrets redacted.

### Phase C — Plan/apply v2

Deliver:

• application deployment service;
• immutable plans;
• stale-plan validation;
• idempotency;
• approvals.

Acceptance:

• plan has zero target mutation;
• apply of stale plan fails deterministically;
• replay of apply is safe.

### Phase D — Target SDK v2

Deliver:

• public v2 contract;
• v1 adapter;
• K8s v2;
• SSH v2;
• FTP v2;
• conformance v2.

Acceptance:

• first-party targets pass conformance;
• existing target plugins still function through v1 path.

### Phase E — Release/environment UX

Deliver:

• v2 APIs;
• CLI release/environment/deploy/history;
• MCP parity;
• config translation.

Acceptance:

• a fresh user can create release → plan → deploy → inspect history
without understanding legacy RolloutConfig internals.

### Phase F — Verification/policy unification

Deliver:

• verifier contract;
• HTTP;
• Prometheus migration;
• policy decision points;
• approval requirements;
• rollback integration.

Acceptance:

• failed canary verification can automatically roll back;
• policy behavior identical across CLI/API/MCP.

### Phase G — Drift/reconcile migration

Deliver:

• environment desired state;
• drift events;
• reconcile policy;
• target lease integration.

Acceptance:

• reconcile is distinguishable from user deployment in history;
• no duplicate concurrent reconcile/deploy.

### Phase H — Pipeline foundation

Deliver:

• Pipeline;
• PipelineRun;
• graph runtime;
• local executor;
• command/test nodes.

Acceptance:

```bash
rollops run
```

executes locally with durable/machine-readable run state.

### Phase I — Build/artifact pipeline

Deliver:

• container executor;
• BuildKit;
• artifact outputs;
• cache;
• Release creation from pipeline.

Acceptance:

```text
commit → test → build → immutable artifact → release
```

is first-class.

### Phase J — Remote execution

Deliver:

• worker protocol;
• Kubernetes executor;
• remote runner;
• log streaming;
• worker security.

Acceptance:

same pipeline semantics locally and remotely.

────────

## 38. Coding-Agent Work Rules

A coding agent working on RollOps **MUST**:

1. read this architecture document;
2. read repository `AGENTS.md`;
3. inspect relevant existing packages before creating new ones;
4. identify the phase/workstream being changed;
5. state affected invariants in the PR description;
6. prefer adapting existing code over parallel replacement;
7. add tests proving semantic behavior;
8. run repository formatting, vet, unit tests, race tests where
practical, and relevant integration/conformance suites;
9. update API/config documentation for public changes;
10. avoid drive-by refactors unrelated to the task.

A coding agent **MUST NOT**:

• add a new storage backend abstraction if an existing port can be
extended cleanly;
• create duplicate domain models in transport packages;
• make Kubernetes types part of core domain APIs;
• return secrets in errors;
• mutate state during planning;
• bypass policy for MCP;
• make daemon availability mandatory for local commands;
• add YAML fields without corresponding typed domain semantics;
• introduce an executor before Phase H merely to run deployment target
operations;
• silently break existing RolloutConfig files.

────────

## 39. Definition of Done for a Feature

A feature is done only when applicable items are complete:

• domain semantics defined;
• invariants tested;
• public API types documented;
• storage migration included;
• CLI machine output considered;
• HTTP/gRPC mapping considered;
• MCP mapping considered;
• RBAC permission considered;
• policy decision point considered;
• audit/event emitted;
• secrets redacted;
• cancellation handled;
• idempotency handled for mutations;
• metrics/traces added where operationally relevant;
• target/executor conformance updated if contract changed;
• backwards compatibility tested;
• docs/examples updated.

────────

## 40. Architectural Invariants

These are hard constraints.

**INV-001 — Plan purity**

Planning does not mutate deployment targets.

**INV-002 — Release immutability**

A Release's artifact/source identity never changes after creation.

**INV-003 — Artifact immutability**

Deployments operate on immutable artifact digests.

**INV-004 — Universal policy**

Every caller type uses the same policy engine.

**INV-005 — Universal attribution**

Every mutation has a Principal.

**INV-006 — Transport independence**

Domain behavior does not depend on CLI/API/MCP transport.

**INV-007 — Infrastructure independence**

Core domain packages do not import Kubernetes/cloud provider SDKs.

**INV-008 — No mandatory daemon**

Supported local workflows remain possible in-process.

**INV-009 — No mandatory Git**

GitOps is an integration, not the only mutation path.

**INV-010 — Verifiable deployment**

Apply completion alone does not imply verified success.

**INV-011 — Stale plan rejection**

Apply never silently regenerates a stale plan.

**INV-012 — Secret non-persistence**

Secret values never enter plans, events, audit, logs, or public state.

**INV-013 — Event immutability**

Persisted domain events are append-only.

**INV-014 — Explicit capabilities**

Target/executor features are capability-declared.

**INV-015 — Idempotent external mutation**

Mutation APIs support safe replay semantics.

**INV-016 — Backward migration**

Legacy RolloutConfig is translated, not abruptly discarded.

────────

## 41. Key ADRs to Create

Create ADRs before or during implementation for:

1. UUIDv7 vs ULID.
2. Event persistence in SQLite vs dedicated Bolt continuation.
3. Aggregate/projection transaction model.
4. Target plugin RPC protocol.
5. Config API version naming/domain.
6. Remote executor transport.
7. Artifact store abstraction boundaries.
8. OpenTelemetry semantic conventions.
9. PostgreSQL support threshold.
10. Whether pipeline definitions live in the Project resource or
separate resources.

Do not use ADRs to reopen the product principles in this document
without explicit maintainer intent.

────────

## 42. Example End-to-End Flow

### 42.1 Release creation

Input:

```text
source commit abc123
OCI image ghcr.io/acme/api@sha256:91ab
cosign signature
SLSA provenance
```

Engine:

1. validate project;
2. register/resolve artifact;
3. verify digest;
4. attach provenance;
5. evaluate `release.create`;
6. persist immutable Release;
7. append `release.created`.

### 42.2 Production plan

User/agent:

```bash
rollops plan production --release rel_123
```

Engine:

1. load Release;
2. load Environment;
3. authorize `deployment.plan`;
4. inspect each target;
5. resolve desired target representation;
6. compute diff;
7. build strategy operations;
8. build verification plan;
9. compute rollback target;
10. calculate risk;
11. evaluate policy;
12. persist immutable plan;
13. append `deployment.plan.created`;
14. return plan.

No target mutation occurs.

### 42.3 Apply

```bash
rollops deploy production --plan pln_123
```

Engine:

1. authorize;
2. load plan;
3. validate expiry/hash;
4. validate base revisions;
5. re-evaluate required policy conditions;
6. verify approvals;
7. acquire environment/target lease;
8. create Deployment and append `deployment.queued`;
9. append `deployment.started`;
10. execute operation DAG;
11. observe;
12. verify;
13. promote as strategy permits;
14. final verify;
15. mark success;
16. append final events;
17. release lease.

### 42.4 Failed canary

At 25%:

1. verification returns `fail`;
2. event records evidence;
3. strategy halts promotion;
4. policy/config chooses automatic rollback;
5. rollback plan is activated;
6. previous immutable Release is restored;
7. rollback verification runs;
8. Deployment becomes `rolled_back`;
9. timeline preserves both failure and recovery.

────────

## 43. Example Machine-Readable Plan

```json
{
  "id": "pln_01...",
  "project_id": "prj_01...",
  "environment_id": "env_01...",
  "release_id": "rel_01...",
  "base_revision": 42,
  "operations": [
    {
      "id": "op_1",
      "kind": "target.apply",
      "target": "k8s-production",
      "summary": "Update api image digest",
      "reversible": true
    }
  ],
  "risk": {
    "level": "medium",
    "score": 0.48,
    "factors": [
      {
        "code": "production_environment",
        "message": "Deployment targets a production environment"
      }
    ]
  },
  "policy": {
    "allowed": false,
    "requirements": [
      {
        "type": "approval",
        "role": "production-approver",
        "count": 1
      }
    ]
  },
  "rollback": {
    "release_id": "rel_previous",
    "automatic": true
  }
}
```

This is illustrative, not a frozen wire schema.

────────

## 44. Product Success Criteria

The architecture is succeeding when:

• one release can be traced from source to production;
• Kubernetes and non-Kubernetes deployments share the same
control-plane semantics;
• local and daemon operation produce equivalent plans;
• agents can operate entirely through typed APIs without scraping
prose;
• a production deployment can be safely planned, approved, verified,
and rolled back;
• target authors can implement a provider without importing engine
internals;
• users do not need a central server for basic use;
• CI can later produce the exact same Release objects that CD
consumes;
• adding a new executor does not require changing deployment domain
types;
• adding a new target does not require changing pipeline types.

────────

## 45. Recommended Immediate Backlog

Execute in this order.

**R1 — Introduce domain identity package**

Typed IDs, Principal, revisions, clock/id generator ports.

**R2 — Add Project/Environment/Artifact/Release persistence**

SQLite migrations + repositories + tests.

**R3 — Introduce DeploymentPlan v2**

Immutable plan record, hash, expiry, stale-plan semantics.

**R4 — Wrap existing engine with DeploymentService**

Preserve existing behavior while establishing application boundary.

**R5 — Introduce event envelope and timeline**

Dual-write if necessary; do not remove legacy audit yet.

**R6 — Target SDK v2**

Contract + adapter + conformance.

**R7 — Port Kubernetes target**

Use it to prove the v2 contract.

**R8 — Port SSH and FTP**

Prove the contract is not Kubernetes-shaped.

**R9 — Unify verification**

Move current metric analysis and health verification behind verifier
contracts.

**R10 — Unify policy/approval**

Risk becomes policy input; approvals bind to plan.

**R11 — Add v2 gRPC/HTTP**

Keep v1 compatibility.

**R12 — Add MCP v2 tools**

Plan/explain/apply/status/timeline with structured outputs.

**R13 — Add CLI release/environment model**

Preserve old commands as compatibility aliases where useful.

**R14 — Legacy config translator**

Parity fixtures for existing examples.

Only after R1–R14 are stable should the project start the
Pipeline/Executor work.

────────

## 46. What "Best Open Source CI/CD Tool" Means Here

Do not optimize for the largest feature checklist.

Optimize for these properties:

1. **Coherence** — one model from artifact to production.
2. **Safety** — plan, policy, verification, rollback.
3. **Portability** — Kubernetes, VPS, cloud, local.
4. **Composability** — library + APIs + plugins.
5. **Agent operability** — structured, attributable, safe automation.
6. **Developer experience** — excellent CLI and local execution.
7. **Inspectability** — durable timeline and provenance.
8. **Extensibility** — small stable contracts.
9. **Low operational burden** — single binary + SQLite remains
viable.
10. **Progressive complexity** — users adopt only what they need.

The project should prefer a smaller number of unusually strong
abstractions over hundreds of provider-specific features.

────────

## 47. Final North Star

The conceptual model is:

```text
                     SOURCE
                        │
                        ▼
                     PIPELINE
                        │
                        ▼
                     ARTIFACT
                        │
                        ▼
                     RELEASE
                        │
             ┌──────────┼──────────┐
             ▼          ▼          ▼
           PREVIEW    STAGING     PROD
                         │          │
                         ▼          ▼
                       VERIFY     CANARY
                                    │
                                    ▼
                                  VERIFY
                                    │
                              ┌─────┴─────┐
                              ▼           ▼
                           PROMOTE     ROLLBACK
```

The implementation model is:

```text
                  CLI / API / MCP / Git / UI
                           │
                           ▼
                    Application Layer
                           │
              ┌────────────┼────────────┐
              ▼            ▼            ▼
           Domain       Policy       Planner
              │                         │
              └────────────┬────────────┘
                           ▼
                      Delivery Graph
                           │
             ┌─────────────┼─────────────┐
             ▼             ▼             ▼
           Targets      Verifiers     Executors
                                      (phase 2)
             │             │             │
             └─────────────┼─────────────┘
                           ▼
                    Events + State
                           │
                           ▼
                 History / Audit / UI
```

RollOps is the open-source control plane for shipping software.

Everything else is an adapter, provider, execution mechanism, or
projection around that core.

────────

## 48. Reference Baseline

Public baseline used when preparing this specification:

• Go package/module documentation:
https://pkg.go.dev/go.klarlabs.de/rollops
• Repository: https://github.com/klarlabs-studio/rollops

The repository remains the authority for current implementation details.
This document is the authority for the intended next architecture unless
superseded by a newer architecture specification or accepted ADR.
