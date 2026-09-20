// Command rollops is the Rollops CLI. In one-shot mode it links the engine
// in-process (no daemon required - good for local use, CI, and recovery); a
// ROLLOPS_DAEMON selects gRPC-client mode against a running daemon. The command
// surface is identical across both modes (internal/cli).
package main

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.klarlabs.de/rollops/internal/api/v2/cliapi"
	"go.klarlabs.de/rollops/internal/boot"
	"go.klarlabs.de/rollops/internal/cli"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/governance"
	"go.klarlabs.de/rollops/internal/grpcapi"
	"go.klarlabs.de/rollops/internal/notify"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	"go.klarlabs.de/rollops/internal/target"
)

func main() {
	err := run(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "rollops:", err)
	}
	// The exit status is classified rather than flattened to 1, because spec
	// 26.4 makes it contract: a script has to be able to tell "the gate is
	// waiting for an approver" from "the deployment failed" from "the system
	// could not be reached" without reading English. cliapi.Exit answers 0 for
	// a nil error, so success still leaves through the same door.
	os.Exit(int(cliapi.Exit(err)))
}

func run(args []string) error {
	dbPath := os.Getenv("ROLLOPS_DB")
	if dbPath == "" {
		dbPath = "rollops.db"
	}
	daemonAddr := os.Getenv("ROLLOPS_DAEMON")
	token := os.Getenv("ROLLOPS_TOKEN")
	app := &cli.App{
		Out:   os.Stdout,
		Actor: localUser(),
		Doctor: cli.Doctor{
			DBPath:     dbPath,
			DaemonAddr: daemonAddr,
			Token:      token,
			Probe:      probeDaemon,
		},
	}
	app.Doctor.Notifier, app.Doctor.NotifyChannels = notify.FromEnv(os.Getenv)
	app.Doctor.Governor = governance.FromEnv(os.Getenv)
	app.Doctor.GovernorURL = os.Getenv("ROLLOPS_GOVERNANCE_URL")

	// doctor and plugin need no engine/daemon — dispatch them directly.
	if len(args) > 0 && (args[0] == "doctor" || args[0] == "plugin") {
		return app.Run(context.Background(), args)
	}

	// Daemon mode: if ROLLOPS_DAEMON points at a running daemon, drive it over
	// gRPC. Otherwise run the engine in-process (one-shot). Identical surface.
	if daemonAddr != "" {
		client, err := grpcapi.Dial(daemonAddr, token)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		app.Ops = client
		return app.Run(context.Background(), args)
	}

	db, err := sqlite.Open(dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	opts, err := boot.Config{Getenv: os.Getenv, Store: db, Log: os.Stderr}.Options(ctx)
	if err != nil {
		return err
	}
	app.Ops = cli.EngineOps{Engine: engine.New(db, target.Builtin(), opts...), Actor: app.Actor}

	// The release model runs over the same database as the rollout engine, from
	// the same process. Building it here rather than behind a flag is what keeps
	// the two surfaces from being two installations.
	svc, err := boot.V2(ctx, boot.V2Config{Getenv: os.Getenv, Store: db, Log: os.Stderr})
	if err != nil {
		return err
	}
	app.V2, err = cliapi.New(cliapi.Config{
		Service:  svc,
		Identity: cliapi.IdentifierFunc(localPrincipal),
		Out:      os.Stdout,
	})
	if err != nil {
		return err
	}
	return app.Run(ctx, args)
}

func probeDaemon(ctx context.Context, addr, token string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	client, err := grpcapi.Dial(addr, token)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	_, err = client.Status(ctx, "__rollops_doctor_probe__")
	switch status.Code(err) {
	case codes.NotFound:
		return nil // authenticated and reached the daemon.
	case codes.Unauthenticated:
		return fmt.Errorf("unauthorized token")
	default:
		return err
	}
}

// localUser is the invoking identity; the one-shot CLI inherits the local user
// and cannot bypass the gate or RBAC.
func localUser() rollout.Identity {
	name := "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	return rollout.Identity{Kind: "human", Name: name}
}

// localPrincipal is the same identity in the release model's vocabulary. It is
// read from the operating system rather than from an argument for the reason
// cliapi.Identifier exists: a --actor flag would let anybody attribute a
// deployment to somebody else, and INV-005 makes attribution part of the
// record.
//
// No claims are attached. Nothing downstream reads them here, and a Principal
// that carries none cannot leak one (INV-012).
func localPrincipal(context.Context) (identity.Principal, error) {
	name := "unknown"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	return identity.Principal{ID: name, Type: identity.PrincipalHuman, DisplayName: name}, nil
}
