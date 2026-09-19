# ADR-0006 — Target contract v2 is a typed RPC whose capabilities are declared, not asserted

- **Status:** accepted
- **Date:** 2026-09-19
- **Spec:** `docs/architecture/rollops-next.md` §7.2, §9, §24, §33 · **Backlog:** R6
- **Invariants touched:** INV-007 (infrastructure independence), INV-012 (secret non-persistence), INV-014 (explicit capabilities)
- **Decides:** pending decision #4

## Context

§9 asks for a deployment-oriented Target contract with an explicit capability
struct (§9.3), a mandatory idempotency key (§9.4), ten conformance axes (§9.5)
and an adapter so v1 implementations keep working (§9.6). §33.3 requires "a
versioned RPC protocol" for out-of-process plugins and forbids Go's `plugin`
package, but does not name a transport.

Four things about what exists today shape the decision.

**A type assertion does not cross a process boundary, and the code assumes it
does.** v1 declares optional capabilities as Go subinterfaces — `Differ`,
`Renderer`, `Inspector`, `Reaper`, `Preflighter` — and six production sites ask
for them by assertion: `internal/engine/engine.go:344`, `:405`, `:1748`,
`:1766`, `:2049`, and `internal/reconcile/reap.go:266`. The plugin adapter,
`internal/target/plugin/build.go`, implements exactly four methods — `Apply`,
`Observe`, `Health`, `Close`. Every one of those six assertions therefore
returns false for **every** plugin-backed target, however capable the plugin
is, and the caller's fallback path is indistinguishable from "this target
genuinely cannot". A plugin that can diff has no way to say so. This is
precisely the failure §9.3's explicit struct exists to prevent, and today it is
a silent one.

**The wire describes nothing.** `proto/rollops/plugin/v1/plugin.proto` defines
one generic service: `GetManifest`, and `InvokeTool(capability, tool, bytes
input) → bytes output`, where the bytes are JSON. The real contract lives in
`pkg/plugin/wire.go` as Go structs with JSON tags. So protobuf carries an
opaque payload, `protoc` checks nothing, and a field added on one side is
dropped by the other with no error anywhere. §9.4's typed conflict and §9.5's
typed error mapping cannot be expressed in `bytes` at all.

**Two different things are both called "capability".** In §9.3 a capability is
a *feature* (`DriftDetection`, `NativeRollback`) — a boolean. In
`pkg/plugin/manifest.go` a `Capability` is a *namespace of tools* (`target`,
`featureflag`) — a routing key. The words collide on the wire, in the builder
API, and in `pluginhost.HasCapability`.

**Idempotency is absent.** `git grep -i idempotenc` finds two doc comments and
`pkg/conformance.CheckIdempotent`, which checks that a second `Apply` reports
`Changed=false`. That is idempotence of *effect*, which is a good property and
not the one §9.4 asks for: there is no key, so a retry after a crash cannot be
distinguished from a new request.

`pkg/conformance` covers roughly two and a half of §9.5's ten axes.

## Decision

### 1. v2 is a typed gRPC service beside the generic one, not a new transport.

Launch, handshake, artifact pinning, manifest fetch and safety validation stay
exactly as they are: a subprocess, one stdout handshake line, a unix socket, a
`GetManifest` gated by `pluginhost.Policy.Validate` before any call. That
machinery is the part §33.3 and §33.4 are actually about, and it works.

What changes is what is served over the socket. A v2 target plugin serves
**two** gRPC services on the same server: the existing
`rollops.plugin.v1.Plugin` for the manifest, and a new
`rollops.target.v2.Target` with one RPC per §9.2 method. `bytes`-of-JSON is
gone from the target path.

Per §24, this is a new package rather than a mutation of the old messages:
proto package `rollops.target.v2`, generated to `pkg/target/rollopstargetv2`,
with the Go contract at `pkg/target/v2` per §7.2's layout. `rollops.plugin.v1`
is not extended for target purposes and no removed field number is reused. The
other plugin kinds — `featureflag`, `trafficrouter`, `metricprovider` — keep
using the generic `InvokeTool` wire until each gets a contract of its own; this
ADR does not decide their shape.

### 2. The handshake version stops doing double duty; the manifest carries the contract version.

`plugin.ProtocolVersion` stays `1` and `Handshake.Verify` stays an equality
check, because the thing it versions — launch, cookie, socket advertisement,
manifest fetch — is not changing. The version that says `2` is the *contract*
version, and §9.6 step 4 already says where it belongs: the manifest advertises
it. `GetManifestResponse` gains a repeated `DeclaredContract {string kind;
int32 version;}` at a fresh field number, so `target/2` is something the host
reads before it dials anything typed.

This is why the handshake can keep an equality check that would otherwise be
too strict: one number now means one thing.

### 3. Capabilities are declared in the manifest, confirmed at runtime, and the manifest is a ceiling.

Both §33.2 (`capabilities: [drift, rollback]` in the manifest) and §9.2
(`Capabilities(context.Context) (Capabilities, error)`) exist, because they
answer different questions. The manifest says what an operator authorized when
they installed this binary — §33.4's "requested capabilities", the thing that
is not the same as runtime authorization. The runtime call says what this
*bound* target can do right now, which the manifest cannot know: a Kubernetes
plugin against a cluster with no Rollout CRD can honestly declare progressive
delivery in general and not have it here.

**The effective capability set is the intersection, and the manifest is the
ceiling.** A runtime claim the manifest does not cover is dropped, not honoured
— the same fail-closed posture `policy.Validate` already takes with safety
scopes. The drop is surfaced rather than swallowed: a plugin claiming more at
runtime than it was installed with is a trust signal, and §33.4 is the section
that says so.

The narrowing direction is the one that can be made safe. Refusing outright
instead would mean a plugin that gains a capability in a patch release breaks
every existing install until each operator re-authorizes; narrowing means it is
simply not used until they do.

Manifest capability names are strings with one canonical name per `Capabilities`
field. A name the host does not recognise is ignored — a ceiling that mentions
something unknown caps nothing, so forward compatibility here costs nothing.

### 4. Optional methods are mandatory on the wire and answer `ErrUnsupported`.

§9.2 says optional capabilities **SHOULD** use subinterfaces rather than
ever-growing mandatory methods. That is honoured as a statement about how the
contract is *written*: `Promoter`, `Drifter` and `Pruner` exist as named Go
types, they document intent, and an in-process target implements the ones it
supports.

It is explicitly **not** honoured as a statement about how support is
*detected*:

- The `rollops.target.v2.Target` service declares every method, optional ones
  included. A plugin that does not implement one returns `codes.Unimplemented`,
  which the host maps to a typed `target.ErrUnsupported{Capability: …}`.
- The host-side adapter implements **every** subinterface unconditionally.
  `tgt.(Drifter)` therefore always succeeds and tells the caller nothing.
- Callers **MUST** consult `Capabilities(ctx)`. A type assertion used to decide
  whether a capability is available is a bug, and the six sites listed above
  are the existing instances of it.

The alternative readings both fail. Conditional wrapper types — synthesising a
Go type per capability combination so the assertion means something — needs 2ⁿ
types for n capabilities, 64 at §9.3's six, and is the `http.ResponseWriter`
problem that the standard library has spent two decades apologising for.
Dynamic dispatch by reflection moves the same lie behind a slower mechanism.
Making the struct authoritative is the only shape that reads identically for an
in-process target and a subprocess, which is what INV-007 asks for.

There is one place a type assertion stays correct, and it is instructive: the
v1 adapter (decision 6) derives its `Capabilities` by asserting the v1
subinterfaces on a value it holds in its own process. Assertions are right
exactly where both sides are in the same address space. The bug was using one
where they are not.

### 5. Conformance makes a capability lie a test failure.

§9.5's capability-truthfulness axis is checked **in both directions**: every
capability declared true must be callable, and every capability declared false
must return `ErrUnsupported` rather than a plausible-looking empty result. The
second half is the one that would have caught today's bug — a `DriftResult`
with no drift and a target that cannot detect drift are the same value, and
only the error tells them apart.

`pkg/conformance` grows from three checks to §9.5's ten axes and takes a v2
factory. §9.6 step 3 requires it to support both contracts; it does so by
wrapping a v1 factory through the adapter and running the same suite, so "v1
target" and "v2 target that declares fewer capabilities" are the same case.

### 6. The idempotency key is minted by RollOps from durable state.

`ApplyRequest.IdempotencyKey` is required, and the host adapter rejects an
empty one before the call — validating at the boundary we control rather than
trusting every plugin to.

It is derived from the deployment ID and the planned operation ID. Both are
durable and both are already stable across a retry, which matters because the
case §9.4 exists for is a crash between the call and the response: a key minted
at call time is regenerated by the retry and defeats itself, where a key
derived from stored state is the same key. Two deployments that happen to plan
an identical change get different keys, which is correct — they are two
intents.

Same key and same request must replay the same semantic result. Same key and a
*different* request is a typed `ErrIdempotencyConflict` carrying the key. A
provider that cannot guarantee replay returns the conflict rather than applying
twice, which is §9.4's second branch and the reason it is a branch.

### 7. `Health` is absorbed into `Observe`.

§9.2 lists no `Health` method. `Observation` carries the health state, and
`HealthObservation` is a declared capability, so "this target cannot tell you
whether it is healthy" becomes expressible — which it is not today, where
`Health` is mandatory and a target with nothing to say must invent an answer.
v1's `Health` maps through the adapter; `HealthState`'s values are unchanged.

## Consequences

- **The silent-capability bug is fixed as a consequence of the contract, not as
  a patch.** Each of the six assertion sites becomes a `Capabilities` lookup.
  Until R6 lands they remain wrong, and a plugin-backed target keeps losing
  diff, render, inspect, reap and preflight without saying so. This ADR does not
  fix it; it is the reason the fix is shaped the way it is.
- **Every target grows a method it has to think about.** `Capabilities` cannot
  be defaulted to "everything" without reintroducing the lie in the other
  direction, so a new target author now answers six questions before anything
  compiles. That is the intended cost of INV-014.
- **Two contracts are alive at once for at least one major version**, per §9.6.
  `pkg/target` (v1) and `pkg/target/v2` both exist, conformance runs both, and
  v1 warns. v1 is removed only at a major boundary — this ADR does not schedule
  that.
- **`internal/engine` gets worse before it gets better.** It is written against
  v1 `Target` throughout; R6 introduces v2 and the adapter, and R7 (Kubernetes)
  is what proves the contract by porting a real target. An engine that speaks v2
  natively is a later change, and until then first-party targets travel through
  the adapter in the opposite direction from the one §9.6 names.
- **Proto generation is now two packages**, and `.coverctl.yaml`'s exclusion
  list grows a third generated path beside `internal/grpcapi/rollopsv1/*` and
  `pkg/plugin/rollopspluginv1/*`.
- **The `capability` collision survives on the v1 wire.** §24 forbids reusing or
  repurposing field numbers, so `rollops.plugin.v1.Capability` keeps its name
  and meaning — a tool namespace — forever. The v2 vocabulary says *contract*
  for the namespace and *capability* for the boolean. Anyone reading both files
  in one sitting will need this paragraph.
- **Nothing here changes what a plugin is allowed to do.** Safety scopes, risk
  class, artifact pinning and the policy gate are untouched; a capability is
  what a plugin *can* do, and §33.4's separation between installation and
  runtime authorization is why the manifest ceiling exists rather than being
  redundant with it.

## Alternatives rejected

- **Keep `InvokeTool` and add target v2 tools to it.** Cheapest, and it keeps
  the JSON tunnel: no schema, no typed errors, no way to express a conflict, and
  a silent drop on every field mismatch. §9.5 lists typed error mapping as a
  conformance axis, which a `bytes` payload cannot satisfy.
- **A capability struct returned only from the manifest.** One round trip, and
  the answer is per *binary* rather than per *bound target*, so the Rollout-CRD
  case has no representation and a plugin must either over-claim or under-claim
  for every environment it is ever pointed at.
- **A capability struct returned only at runtime.** Loses §33.4's install-time
  trust surface: an operator would authorize an executable without being told
  what it intends to do, and "plugin installation is not equivalent to runtime
  authorization" becomes untestable because there is nothing at install time to
  compare against.
- **Conditional wrapper types so type assertions keep working.** 2ⁿ generated
  types, and it preserves the idea that a type assertion answers a question
  about a remote process. It does not; that is the whole bug.
- **The plugin mints the idempotency key.** Then the key identifies the *call*
  rather than the *intent*, and a retry is a new call, which is the one case the
  key exists to catch.
- **A non-gRPC transport** (JSON-RPC over the same socket, or stdio framing).
  §33.3 requires only "a versioned RPC protocol", so this was open. gRPC is
  already in the module graph, already generates both sides, already carries
  deadlines and cancellation — two of §9.5's ten axes — and already has
  `codes.Unimplemented`, which decision 4 needs. Changing transports would spend
  the plugin ecosystem's one compatibility break on something the current
  transport was not the problem with.
