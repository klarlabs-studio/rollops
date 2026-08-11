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

	"go.klarlabs.de/rollops/internal/cli"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/governance"
	"go.klarlabs.de/rollops/internal/grpcapi"
	"go.klarlabs.de/rollops/internal/notify"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	"go.klarlabs.de/rollops/internal/target"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rollops:", err)
		os.Exit(1)
	}
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

	// The in-process engine honors external governance too. Wiring it only into the
	// daemon would leave `rollops apply` on a laptop as the way around the gate, and a
	// gate you can walk around is not one.
	var engOpts []engine.Option
	if g := governance.FromEnv(os.Getenv); g != nil {
		engOpts = append(engOpts, engine.WithGovernance(g))
	}
	app.Ops = cli.EngineOps{Engine: engine.New(db, target.Builtin(), engOpts...), Actor: app.Actor}
	return app.Run(context.Background(), args)
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
