// Package mcpapi is the MCP projection of the v2 service (§23.1, §25).
//
// It carries tool arguments in and structured results out and decides nothing
// else. Every rule about what a call means lives in internal/api/v2, because a
// rule written here is a rule the gRPC server and the HTTP handler do not have.
// What is genuinely this package's own is narrow: which tool names which call,
// what an agent is handed back, and how a failure code reaches a model that
// only ever sees an error string.
//
// The audience is the difference. gRPC and HTTP answer a program that already
// knows what it wants; an agent is deciding, so §25.2 asks every result to
// carry stable ids, the current state, what may be done next, the policy
// verdict, the risk, and whether an approval is outstanding. None of those are
// invented here — next actions and the approval flag are derived in api/v2, so
// the other two transports can offer the same answers rather than a second set.
//
// This is not a privileged bypass (§25.1). The tools are the same operations,
// authorized the same way, and they are deliberately not all of them: nothing
// here approves a deployment. An agent that could grant the approval its own
// plan is waiting for would not be passing a gate, it would be holding both
// sides of one. Requesting an approval is an action it may name; giving one is
// not a tool it has.
//
// Secrets never arrive (§25.2, INV-012), and not because this package strips
// them: the views it projects already carry configuration keys without values,
// principals without claims, and sensitive changes with both sides blank. What
// this package must not do is add a field that reaches past them.
package mcpapi

import (
	"context"
	"errors"

	mcpserver "go.klarlabs.de/mcp"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/domain/identity"
)

// Identifier says who is calling. It takes a context rather than a request
// because MCP hands a handler nothing else: the transport resolves the caller
// once per connection and the context is what reaches the tool. It returns an
// error rather than a bool so that an implementation which has already worked
// out what kind of refusal this is can say so with an *apierr.Error.
type Identifier interface {
	Identify(ctx context.Context) (identity.Principal, error)
}

// IdentifierFunc adapts a function to Identifier.
type IdentifierFunc func(context.Context) (identity.Principal, error)

// Identify implements Identifier.
func (f IdentifierFunc) Identify(ctx context.Context) (identity.Principal, error) { return f(ctx) }

// Config names what the tools serve and who is trusted to say who is calling.
type Config struct {
	// Service answers every tool. Required.
	Service *apiv2.Service

	// Identity resolves the caller. Required, and there is deliberately no
	// default: a tool surface that authenticates when configured to would
	// serve anonymously when somebody forgot to.
	Identity Identifier
}

// tools holds what every handler needs. It is unexported because the handlers
// are reached through the registered server rather than called directly — the
// schema an agent is shown and the decoding it goes through are part of what
// this package is, and a test that skipped them would not be testing the tools.
type tools struct {
	svc *apiv2.Service
	who Identifier
}

// ServerName and ServerVersion identify this surface to a client.
const (
	ServerName    = "rollops"
	ServerVersion = "2"
)

// New returns an MCP server with the v2 tools registered, naming the first
// dependency it was not given.
func New(cfg Config) (*mcpserver.Server, error) {
	srv := mcpserver.NewServer(mcpserver.ServerInfo{
		Name:         ServerName,
		Version:      ServerVersion,
		Capabilities: mcpserver.Capabilities{Tools: true},
	})
	if err := Register(srv, cfg); err != nil {
		return nil, err
	}
	return srv, nil
}

// Register wires the v2 tools onto an existing server, so that a composition
// root serving one MCP endpoint does not need a second one for this surface.
func Register(srv *mcpserver.Server, cfg Config) error {
	switch {
	case cfg.Service == nil:
		return errors.New("mcpapi: no service")
	case cfg.Identity == nil:
		return errors.New("mcpapi: no identifier")
	}
	t := &tools{svc: cfg.Service, who: cfg.Identity}

	srv.Tool("rollops.inspect_environment").
		Description("Read one environment — its targets, the policies in force, " +
			"the variable names it sets, and its most recent deployments").
		Handler(t.InspectEnvironment)
	srv.Tool("rollops.list_releases").
		Description("List a project's releases, newest first").
		Handler(t.ListReleases)
	srv.Tool("rollops.create_release").
		Description("Fix a set of registered artifacts as a named, immutable release").
		Handler(t.CreateRelease)
	srv.Tool("rollops.plan_deployment").
		Description("Work out what deploying a release to an environment would do, " +
			"and what policy says about it. Changes nothing").
		Handler(t.PlanDeployment)
	srv.Tool("rollops.explain_plan").
		Description("Re-read a plan: its operations, the policy verdict, the risk, " +
			"whether an approval is outstanding, and how it would be rolled back").
		Handler(t.ExplainPlan)
	srv.Tool("rollops.apply_plan").
		Description("Admit a deployment for a plan. Refused if the plan has gone " +
			"stale or policy has not cleared it").
		Handler(t.ApplyPlan)
	srv.Tool("rollops.get_deployment").
		Description("Read one deployment's current state and what may be done to it next").
		Handler(t.GetDeployment)
	srv.Tool("rollops.verify_deployment").
		Description("Run the environment's checks against a deployment and report " +
			"each verdict. Observes; changes no substrate").
		Handler(t.VerifyDeployment)
	srv.Tool("rollops.promote_deployment").
		Description("Widen a deployment past its checks. A paused deployment needs " +
			"a reason, because promoting it disregards whatever paused it").
		Handler(t.PromoteDeployment)
	srv.Tool("rollops.rollback_deployment").
		Description("Return a deployment to the release it replaced, using the " +
			"rollback operations its plan recorded").
		Handler(t.RollbackDeployment)
	srv.Tool("rollops.cancel_deployment").
		Description("Stop a deployment that has not finished. Undoes nothing — " +
			"whatever landed stays landed. A reason is required").
		Handler(t.CancelDeployment)
	srv.Tool("rollops.get_timeline").
		Description("Read a deployment's event log, oldest first").
		Handler(t.GetTimeline)

	return nil
}

// ToolError is the typed failure §25.2 asks for.
//
// MCP gives a handler one way to fail — an error whose text reaches the model —
// so the stable code is put in front of the message rather than left to be
// inferred from prose. An agent branching on APPROVAL_REQUIRED against
// POLICY_DENIED is reading this, and those two differ by what it should do
// next, not by how badly the call went.
type ToolError struct {
	Code    apierr.Code
	Message string

	err error
}

func (e *ToolError) Error() string { return string(e.Code) + ": " + e.Message }

func (e *ToolError) Unwrap() error { return e.err }

// fail classifies err into the failure an agent is told about. A nil error
// stays nil so that handlers can return it unconditionally.
func fail(err error) error {
	if err == nil {
		return nil
	}
	e := apierr.From(err)
	return &ToolError{Code: e.Code, Message: e.Message, err: e}
}

// caller resolves who is calling, or the refusal to say so.
//
// An Identifier that has already decided what kind of refusal this is keeps its
// answer, for the reason grpcapi gives: telling a caller whose credential is
// fine but whose permissions are not to go and authenticate again will not
// help. The classification is made here rather than shared with the other
// transports because neither should depend on the other.
func (t *tools) caller(ctx context.Context) (identity.Principal, error) {
	who, err := t.who.Identify(ctx)
	if err == nil {
		return who, nil
	}
	var already *apierr.Error
	if errors.As(err, &already) {
		return identity.Principal{}, fail(already)
	}
	return identity.Principal{}, fail(&apierr.Error{
		Code:    apierr.Unauthorized,
		Message: "the caller was not identified",
		Err:     err,
	})
}

// reader identifies a caller whose principal the call does not otherwise need.
// Reads are identified too: answering an agent that has not said who it is with
// a not-found would tell it which ids exist.
func (t *tools) reader(ctx context.Context) error {
	_, err := t.caller(ctx)
	return err
}
