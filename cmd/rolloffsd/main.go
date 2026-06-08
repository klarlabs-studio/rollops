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

	mcpserver "go.klarlabs.de/mcp"

	"go.klarlabs.de/rolloffs/internal/api"
	"go.klarlabs.de/rolloffs/internal/engine"
	"go.klarlabs.de/rolloffs/internal/mcp"
	"go.klarlabs.de/rolloffs/internal/metrics"
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
	// Agents may plan/apply/status to non-prod by default; prod stays human-gated.
	policy.DefineRole(security.Role{Name: "agent", Grants: []security.Grant{
		{Perm: security.PermPlan}, {Perm: security.PermStatus},
		{Perm: security.PermApply, Scope: security.Scope{Env: "dev"}},
		{Perm: security.PermApply, Scope: security.Scope{Env: "staging"}},
	}})
	policy.Bind("agent:*", "agent")

	// Self-observability: /metrics and /readyz unauthenticated for scrapers,
	// everything else behind the authenticated API.
	met := metrics.New()
	top := http.NewServeMux()
	top.Handle("/metrics", met.Handler())
	top.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	top.Handle("/", api.New(eng, auth, policy).Handler())

	srv := &http.Server{Addr: addr, Handler: top}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Reconcile tick: fire due schedules. Git-watch reconciliation wires the
	// reconciler over watched repos (internal/reconcile) on the same tick.
	go scheduleLoop(ctx, eng)

	// MCP agent surface, embedded by default when an address is configured.
	if mcpAddr := os.Getenv("ROLLOFFS_MCP_ADDR"); mcpAddr != "" {
		agent := rollout.Identity{Kind: "agent", Name: envOr("ROLLOFFS_MCP_AGENT", "local")}
		tools := mcp.NewTools(eng, policy, agent)
		go func() {
			fmt.Fprintf(os.Stderr, "rolloffsd: MCP serving on %s as agent %q\n", mcpAddr, agent.Name)
			_ = mcpserver.ServeHTTP(ctx, mcp.NewServer(tools), mcpAddr)
		}()
	}

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
