// Command rolloffs is the Rolloffs CLI. In one-shot mode it links the engine
// in-process (no daemon required — good for local use, CI, and recovery); a
// future --addr flag selects gRPC-client mode against a running daemon. The
// command surface is identical across both modes (internal/cli).
package main

import (
	"context"
	"fmt"
	"os"
	"os/user"

	"go.klarlabs.de/rolloffs/internal/cli"
	"go.klarlabs.de/rolloffs/internal/engine"
	"go.klarlabs.de/rolloffs/internal/rollout"
	"go.klarlabs.de/rolloffs/internal/store/sqlite"
	"go.klarlabs.de/rolloffs/internal/target"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rolloffs:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	dbPath := os.Getenv("ROLLOFFS_DB")
	if dbPath == "" {
		dbPath = "rolloffs.db"
	}
	db, err := sqlite.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()

	eng := engine.New(db, target.Builtin())
	app := &cli.App{
		Ops:   eng,
		Out:   os.Stdout,
		Actor: localUser(),
	}
	return app.Run(context.Background(), args)
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
