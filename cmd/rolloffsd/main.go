// Command rolloffsd is the Rolloffs daemon: the always-on reconciler that fires
// due schedules and serves the engine behind an authenticated HTTP/JSON API
// (the grpc-gateway REST front for the UI). It links the same engine library as
// the one-shot CLI, so the two paths stay behaviourally identical.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.klarlabs.de/rolloffs/internal/api"
	"go.klarlabs.de/rolloffs/internal/engine"
	"go.klarlabs.de/rolloffs/internal/rollout"
	"go.klarlabs.de/rolloffs/internal/security"
	"go.klarlabs.de/rolloffs/internal/store/sqlite"
	"go.klarlabs.de/rolloffs/internal/target"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "rolloffsd:", err)
		os.Exit(1)
	}
}

func run() error {
	dbPath := envOr("ROLLOFFS_DB", "rolloffs.db")
	addr := envOr("ROLLOFFS_ADDR", ":8080")

	db, err := sqlite.Open(dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	eng := engine.New(db, target.Builtin())

	// A single bootstrap admin token from the environment; production swaps this
	// for mTLS / an external IdP. Never anonymous.
	auth := api.TokenAuth{}
	if tok := os.Getenv("ROLLOFFS_ADMIN_TOKEN"); tok != "" {
		auth[tok] = rollout.Identity{Kind: "human", Name: "admin"}
	}
	policy := security.NewPolicy()
	policy.DefineRole(security.Role{Name: "admin", Grants: []security.Grant{
		{Perm: security.PermPlan}, {Perm: security.PermApply}, {Perm: security.PermApprove},
		{Perm: security.PermRollback}, {Perm: security.PermStatus}, {Perm: security.PermSchedule},
	}})
	policy.Bind("human:admin", "admin")

	srv := &http.Server{Addr: addr, Handler: api.New(eng, auth, policy).Handler()}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Reconcile tick: fire due schedules. Git-watch reconciliation wires the
	// reconciler over watched repos (internal/reconcile) on the same tick.
	go scheduleLoop(ctx, eng)

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx) // graceful drain on SIGTERM
	}()

	fmt.Fprintf(os.Stderr, "rolloffsd: listening on %s (db %s)\n", addr, dbPath)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func scheduleLoop(ctx context.Context, eng *engine.Engine) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			_, _ = eng.FireDueSchedules(ctx, now.UTC())
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
