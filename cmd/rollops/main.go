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

	"go.klarlabs.de/rollops/internal/boot"
	"go.klarlabs.de/rollops/internal/cli"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/governance"
	"go.klarlabs.de/rollops/internal/grpcapi"
	"go.klarlabs.de/rollops/internal/notify"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	"go.klarlabs.de/rollops/internal/target"
	"go.klarlabs.de/rollops/internal/version"
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
		err = app.Run(context.Background(), args)
		// The daemon runs the rollouts; the CLI only asks. A daemon on another
		// version is running different rollout logic from the one this binary
		// describes, which is how a fixed client sat in front of an unfixed
		// daemon for six releases. Reported after the command so it never
		// replaces the command's own output.
		if got, ok := client.DaemonVersion(); ok && got != version.Version {
			fmt.Fprintf(os.Stderr, "rollops: this client is %s but the daemon at %s is %s — "+
				"the daemon runs the rollouts, so update it "+
				"(deploy/kubernetes/rollopsd-deployment.yaml pins the image)\n",
				version.Version, daemonAddr, got)
		}
		return err
	}

	db, err := sqlite.Open(dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	opts, err := boot.Config{Getenv: os.Getenv, Store: db, Log: os.Stderr}.Options(context.Background())
	if err != nil {
		return err
	}
	app.Ops = cli.EngineOps{Engine: engine.New(db, target.Builtin(), opts...), Actor: app.Actor}
	return app.Run(context.Background(), args)
}

func probeDaemon(ctx context.Context, addr, token string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	client, err := grpcapi.Dial(addr, token)
	if err != nil {
		return "", err
	}
	defer func() { _ = client.Close() }()
	_, err = client.Status(ctx, "__rollops_doctor_probe__")
	daemon, _ := client.DaemonVersion()
	switch status.Code(err) {
	case codes.NotFound:
		return daemon, nil // authenticated and reached the daemon.
	case codes.Unauthenticated:
		return daemon, fmt.Errorf("unauthorized token")
	default:
		return daemon, err
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
