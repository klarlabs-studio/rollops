// Command rolloffsd is the Rolloffs daemon: the always-on reconciler that fires
// due schedules and serves the engine behind an authenticated HTTP/JSON API
// (the grpc-gateway REST front for the UI). It links the same engine library as
// the one-shot CLI, so the two paths stay behaviourally identical.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	mcpserver "go.klarlabs.de/mcp"

	"go.klarlabs.de/rolloffs/internal/api"
	"go.klarlabs.de/rolloffs/internal/audit"
	"go.klarlabs.de/rolloffs/internal/config"
	"go.klarlabs.de/rolloffs/internal/engine"
	"go.klarlabs.de/rolloffs/internal/grpcapi"
	"go.klarlabs.de/rolloffs/internal/mcp"
	"go.klarlabs.de/rolloffs/internal/metrics"
	"go.klarlabs.de/rolloffs/internal/reconcile"
	"go.klarlabs.de/rolloffs/internal/rollout"
	"go.klarlabs.de/rolloffs/internal/security"
	"go.klarlabs.de/rolloffs/internal/store/sqlite"
	"go.klarlabs.de/rolloffs/internal/target"
	"go.klarlabs.de/rolloffs/internal/ui"
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
	uiHandler := basicAuth(ui.New(eng, rollout.Identity{Kind: "human", Name: "admin"}).Handler())
	top.Handle("/ui", uiHandler)
	top.Handle("/ui/", uiHandler)
	top.Handle("/", api.New(eng, auth, policy).Handler())

	srv := &http.Server{Addr: addr, Handler: top}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Reconcile tick: fire due schedules.
	go scheduleLoop(ctx, eng)

	// Git-watch reconciliation: if repos are configured (ROLLOFFS_WATCH points to
	// a JSON list of {name,url,branch,path}), watch and reconcile them on a tick.
	if specs, err := loadWatchSpecs(os.Getenv("ROLLOFFS_WATCH")); err != nil {
		return err
	} else if len(specs) > 0 {
		rec := reconcile.New(eng, audit.New(os.Stderr))
		workdir := envOr("ROLLOFFS_WORKDIR", filepath.Join(os.TempDir(), "rolloffs-repos"))
		watcher, err := reconcile.NewWatcher(ctx, rec, workdir, specs)
		if err != nil {
			return err
		}
		go watcher.Run(ctx, 60*time.Second)
		fmt.Fprintf(os.Stderr, "rolloffsd: watching %d repo(s)\n", len(specs))
	}

	// MCP agent surface, embedded by default when an address is configured.
	if mcpAddr := os.Getenv("ROLLOFFS_MCP_ADDR"); mcpAddr != "" {
		agent := rollout.Identity{Kind: "agent", Name: envOr("ROLLOFFS_MCP_AGENT", "local")}
		tools := mcp.NewTools(eng, policy, agent)
		go func() {
			fmt.Fprintf(os.Stderr, "rolloffsd: MCP serving on %s as agent %q\n", mcpAddr, agent.Name)
			_ = mcpserver.ServeHTTP(ctx, mcp.NewServer(tools), mcpAddr)
		}()
	}

	// Typed gRPC surface (CLI daemon mode + agents) on its own port.
	if grpcAddr := os.Getenv("ROLLOFFS_GRPC_ADDR"); grpcAddr != "" {
		lis, err := net.Listen("tcp", grpcAddr)
		if err != nil {
			return err
		}
		gs := grpcapi.NewGRPCServer(grpcapi.New(eng, auth, policy))
		go func() {
			<-ctx.Done()
			gs.GracefulStop()
		}()
		go func() {
			fmt.Fprintf(os.Stderr, "rolloffsd: gRPC on %s\n", grpcAddr)
			_ = gs.Serve(lis)
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

// loadWatchSpecs reads the watched-repo list from a JSON file. Empty path → no
// repos watched (the daemon still serves the API and fires schedules).
func loadWatchSpecs(path string) ([]reconcile.RepoSpec, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read watch config: %w", err)
	}
	var raw []struct {
		Name, URL, Branch, Path string
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse watch config: %w", err)
	}
	specs := make([]reconcile.RepoSpec, 0, len(raw))
	for _, r := range raw {
		specs = append(specs, reconcile.RepoSpec{
			Name:      r.Name,
			URL:       r.URL,
			Ref:       config.RepoRef{Branch: r.Branch, Path: r.Path},
			Initiator: rollout.Identity{Kind: "ci", Name: "reconciler"},
		})
	}
	return specs, nil
}

// basicAuth gates the browser dashboard. Credentials come from
// ROLLOFFS_UI_USER / ROLLOFFS_UI_PASSWORD; if the password is unset the UI is
// refused entirely rather than served anonymously.
func basicAuth(next http.Handler) http.Handler {
	user := envOr("ROLLOFFS_UI_USER", "admin")
	pass := os.Getenv("ROLLOFFS_UI_PASSWORD")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pass == "" {
			http.Error(w, "ui disabled: set ROLLOFFS_UI_PASSWORD", http.StatusForbidden)
			return
		}
		u, p, ok := r.BasicAuth()
		if !ok || subtleEqual(u, user) == false || subtleEqual(p, pass) == false {
			w.Header().Set("WWW-Authenticate", `Basic realm="rolloffs"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func subtleEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
