package cliapi

import (
	"context"
	"errors"
	"io"
	"slices"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/domain/identity"
)

// Identifier says who is running the command.
//
// It is an interface for the reason grpcapi's is: who is calling is established
// by the surface, never taken from the command line. A --actor flag would let
// anybody attribute a deployment to somebody else, and INV-005 makes
// attribution part of the record rather than a hint.
type Identifier interface {
	Identify(ctx context.Context) (identity.Principal, error)
}

// IdentifierFunc adapts a function to Identifier.
type IdentifierFunc func(ctx context.Context) (identity.Principal, error)

// Identify calls f.
func (f IdentifierFunc) Identify(ctx context.Context) (identity.Principal, error) { return f(ctx) }

// Config is what the command surface needs to answer.
type Config struct {
	// Service is the v2 API. Everything the commands report is derived there.
	Service *apiv2.Service

	// Identity says who is running the command.
	Identity Identifier

	// Out is where answers go. Errors do not: a command returns them, and the
	// caller decides what to print and which exit code to end with.
	Out io.Writer
}

// App is the command surface.
type App struct {
	svc *apiv2.Service
	who Identifier
	out io.Writer
}

// New builds the surface, or says what is missing.
func New(cfg Config) (*App, error) {
	if cfg.Service == nil {
		return nil, errors.New("cliapi: a service is required")
	}
	if cfg.Identity == nil {
		// Defaulting to an anonymous caller would attribute every deployment
		// to nobody, which INV-005 exists to prevent.
		return nil, errors.New("cliapi: an identity is required")
	}
	out := cfg.Out
	if out == nil {
		out = io.Discard
	}
	return &App{svc: cfg.Service, who: cfg.Identity, out: out}, nil
}

// Commands are the verbs and nouns spec 26.1 names, in the order it lists them.
// Run ranges over nothing — the switch is the dispatch — but a test does, which
// is what keeps the two in step.
var Commands = []string{
	"project", "environment", "release",
	"plan", "deploy", "status", "verify", "promote", "rollback", "history",
}

// Run dispatches one command and returns its failure, if any. The caller maps
// that to an exit code with Exit; nothing here calls os.Exit, so the surface is
// testable and embeddable.
func (a *App) Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return invalid("rollops: a command is required (one of %s)", list(Commands))
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "project":
		return a.project(ctx, rest)
	case "environment":
		return a.environment(ctx, rest)
	case "release":
		return a.release(ctx, rest)
	case "plan":
		return a.plan(ctx, rest)
	case "deploy":
		return a.deploy(ctx, rest)
	case "status":
		return a.status(ctx, rest)
	case "verify":
		return a.verify(ctx, rest)
	case "promote":
		return a.promote(ctx, rest)
	case "rollback":
		return a.rollback(ctx, rest)
	case "history":
		return a.history(ctx, rest)
	default:
		return invalid("rollops: unknown command %q (one of %s)", cmd, list(Commands))
	}
}

// caller resolves who is running this command.
//
// Reads resolve it too. A CLI that could read without being identified would
// answer a not-found for an id the caller may not see, which tells them it does
// not exist — and that is a fact about somebody else's project.
func (a *App) caller(ctx context.Context) (identity.Principal, error) {
	who, err := a.who.Identify(ctx)
	if err == nil {
		return who, nil
	}
	var already *apierr.Error
	if errors.As(err, &already) {
		return identity.Principal{}, already
	}
	return identity.Principal{}, &apierr.Error{
		Code:    apierr.Unauthorized,
		Message: "the caller was not identified",
		Err:     err,
	}
}

func (a *App) reader(ctx context.Context) error {
	_, err := a.caller(ctx)
	return err
}

// sub dispatches a noun's operation, or says which ones it has.
func sub(noun string, args []string, known ...string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, invalid("rollops %s: an operation is required (one of %s)", noun, list(known))
	}
	op, rest := args[0], args[1:]
	if !slices.Contains(known, op) {
		return "", nil, invalid("rollops %s: unknown operation %q (one of %s)", noun, op, list(known))
	}
	return op, rest, nil
}

// one takes the single positional argument a command expects, naming what is
// missing rather than reporting an index.
func one(name string, positional []string, what string) (string, error) {
	switch {
	case len(positional) == 0:
		return "", invalid("%s: %s is required", name, what)
	case len(positional) > 1:
		return "", invalid("%s: unexpected argument %q", name, positional[1])
	}
	return positional[0], nil
}

// none refuses a positional argument a command does not take.
func none(name string, positional []string) error {
	if len(positional) > 0 {
		return invalid("%s: unexpected argument %q", name, positional[0])
	}
	return nil
}
