// Command rollopsd is the Rollops daemon: the always-on reconciler that fires
// due schedules and serves the engine behind authenticated HTTP/JSON and gRPC
// APIs. It links the same engine library as the one-shot CLI, so the two paths
// stay behaviourally identical.
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
	"strings"
	"syscall"
	"time"

	"github.com/klarlabs-studio/auth-go/adapters/memory"
	"github.com/klarlabs-studio/auth-go/domain"
	mcpserver "go.klarlabs.de/mcp"

	"go.klarlabs.de/rollops/internal/api"
	"go.klarlabs.de/rollops/internal/audit"
	"go.klarlabs.de/rollops/internal/config"
	"go.klarlabs.de/rollops/internal/engine"
	"go.klarlabs.de/rollops/internal/git"
	"go.klarlabs.de/rollops/internal/grpcapi"
	"go.klarlabs.de/rollops/internal/imageupdate"
	"go.klarlabs.de/rollops/internal/mcp"
	"go.klarlabs.de/rollops/internal/metrics"
	"go.klarlabs.de/rollops/internal/notify"
	"go.klarlabs.de/rollops/internal/reconcile"
	"go.klarlabs.de/rollops/internal/rollout"
	"go.klarlabs.de/rollops/internal/secrets"
	"go.klarlabs.de/rollops/internal/security"
	"go.klarlabs.de/rollops/internal/store"
	"go.klarlabs.de/rollops/internal/store/sqlite"
	"go.klarlabs.de/rollops/internal/target"
	"go.klarlabs.de/rollops/internal/ui"
	"go.klarlabs.de/rollops/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "rollopsd:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) > 0 && (args[0] == "version" || args[0] == "--version") {
		_, _ = fmt.Fprintln(os.Stdout, version.String())
		return nil
	}

	dbPath := envOr("ROLLOPS_DB", "rollops.db")
	addr := envOr("ROLLOPS_ADDR", ":8080")

	db, err := sqlite.Open(dbPath)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	// Full enforced pipeline: audit every action, hard agent guardrails, and
	// secret resolution at execution time. Artifact provenance is enabled when a
	// cosign key is configured.
	aud := audit.New(os.Stderr)
	guard := &security.Guardrails{
		Floor:      security.DefaultPolicyFloor(),
		Freeze:     security.NewFreeze(),
		AgentLimit: security.NewAgentLimiter(20, time.Minute),
	}
	engOpts := []engine.Option{
		engine.WithAudit(aud),
		engine.WithGuardrails(guard),
		engine.WithSecrets(secrets.EnvProvider{Prefix: "ROLLOPS_SECRET_"}),
		engine.WithLeaseOwner(envOr("ROLLOPS_INSTANCE_ID", "rollopsd")),
	}
	if key := os.Getenv("ROLLOPS_COSIGN_KEY"); key != "" {
		engOpts = append(engOpts, engine.WithArtifactGate(security.ArtifactGate{
			Mode:     security.VerifyEnforce,
			Verifier: security.CosignVerifier{KeyPath: key},
		}))
	}
	if n, _ := notify.FromEnv(os.Getenv); n != nil {
		engOpts = append(engOpts, engine.WithNotifier(n))
	}
	eng := engine.New(db, target.Builtin(), engOpts...)

	// A single bootstrap admin token from the environment; production swaps this
	// for mTLS / an external IdP. Never anonymous.
	auth := api.TokenAuth{}
	if tok := os.Getenv("ROLLOPS_ADMIN_TOKEN"); tok != "" {
		auth[tok] = rollout.Identity{Kind: "human", Name: "admin"}
	}
	var httpAuth api.Authenticator = auth
	oidcAuth := buildOIDCAuth()
	if oidcAuth != nil {
		httpAuth = api.ChainAuth{auth, oidcAuth}
	}
	// Build the RBAC policy (bootstrap defaults + optional policy file + OIDC group
	// binds). A bad policy file is fatal at startup — fail closed, not open.
	policy, err := buildPolicy(oidcAuth != nil)
	if err != nil {
		return err
	}
	// Hot-reload on SIGHUP: rebuild and atomically swap. A bad file keeps the
	// current policy (logged), so a typo can't lock everyone out mid-flight.
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGHUP)
		for range ch {
			fresh, err := buildPolicy(oidcAuth != nil)
			if err != nil {
				fmt.Fprintf(os.Stderr, "rollopsd: policy reload failed, keeping current: %v\n", err)
				continue
			}
			policy.ReplaceWith(fresh)
			fmt.Fprintln(os.Stderr, "rollopsd: RBAC policy reloaded")
		}
	}()

	// Self-observability: /metrics and /readyz unauthenticated for scrapers,
	// everything else behind the authenticated API.
	met := metrics.New()
	top := http.NewServeMux()
	top.Handle("/metrics", met.Handler())
	top.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	// "Sync now" triggers an immediate reconcile of the watched repos.
	var watcher *reconcile.Watcher
	uiHandler := uiAuth(ui.New(eng, rollout.Identity{Kind: "human", Name: "admin"},
		ui.WithSync(func(ctx context.Context) error {
			if watcher != nil {
				watcher.Tick(ctx)
			}
			return nil
		})).Handler(), oidcAuth)
	top.Handle("/ui", uiHandler)
	top.Handle("/ui/", uiHandler)
	top.Handle("/", api.New(eng, httpAuth, policy).Handler())

	srv := &http.Server{Addr: addr, Handler: top}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Reconcile tick: fire due schedules.
	go scheduleLoop(ctx, eng)

	// Git-watch reconciliation: if repos are configured (ROLLOPS_WATCH points to
	// a JSON list of {name,url,branch,path}), watch and reconcile them on a tick.
	if specs, err := loadWatchSpecs(os.Getenv("ROLLOPS_WATCH")); err != nil {
		return err
	} else if len(specs) > 0 {
		rec := reconcile.New(eng, aud)
		workdir := envOr("ROLLOPS_WORKDIR", filepath.Join(os.TempDir(), "rollops-repos"))
		watcherOpts := []reconcile.WatcherOption{
			reconcile.WithLogger(func(format string, args ...any) {
				fmt.Fprintf(os.Stderr, "rollopsd: "+format+"\n", args...)
			}),
		}
		if leases, ok := any(db).(store.LeaseStore); ok {
			watcherOpts = append(watcherOpts, reconcile.WithLeaderElection(leases, envOr("ROLLOPS_INSTANCE_ID", "rollopsd"), 2*time.Minute))
		}
		// Registry-poll image automation (replaces keel): configs with an
		// imagePolicy are scanned + bumped back to Git. Registry creds optional.
		if os.Getenv("ROLLOPS_IMAGE_AUTOMATION") != "" {
			watcherOpts = append(watcherOpts, reconcile.WithImageAutomation(&reconcile.ImageAuto{
				Scanner: imageupdate.Scanner{
					Username: os.Getenv("ROLLOPS_REGISTRY_USER"),
					Password: os.Getenv("ROLLOPS_REGISTRY_TOKEN"),
				},
			}))
		}
		w, err := reconcile.NewWatcher(ctx, rec, workdir, specs, watcherOpts...)
		if err != nil {
			return err
		}
		watcher = w
		interval := 60 * time.Second
		if d, err := time.ParseDuration(os.Getenv("ROLLOPS_RECONCILE_INTERVAL")); err == nil && d > 0 {
			interval = d
		}
		go watcher.Run(ctx, interval)
		fmt.Fprintf(os.Stderr, "rollopsd: watching %d repo(s) every %s\n", len(specs), interval)
	}

	// MCP agent surface, embedded by default when an address is configured.
	if mcpAddr := os.Getenv("ROLLOPS_MCP_ADDR"); mcpAddr != "" {
		agent := rollout.Identity{Kind: "agent", Name: envOr("ROLLOPS_MCP_AGENT", "local")}
		tools := mcp.NewTools(eng, policy, agent)
		go func() {
			fmt.Fprintf(os.Stderr, "rollopsd: MCP serving on %s as agent %q\n", mcpAddr, agent.Name)
			_ = mcpserver.ServeHTTP(ctx, mcp.NewServer(tools), mcpAddr)
		}()
	}

	// Typed gRPC surface (CLI daemon mode + agents) on its own port.
	if grpcAddr := os.Getenv("ROLLOPS_GRPC_ADDR"); grpcAddr != "" {
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
			fmt.Fprintf(os.Stderr, "rollopsd: gRPC on %s\n", grpcAddr)
			_ = gs.Serve(lis)
		}()
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx) // graceful drain on SIGTERM
	}()

	fmt.Fprintf(os.Stderr, "rollopsd: listening on %s (db %s)\n", addr, dbPath)
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
		// Auth for a private repo. TokenFile/DeployKeyPath point at files (mount
		// a Secret); Token is the inline value (env-substituted by the operator).
		Token         string `json:"token"`
		TokenFile     string `json:"tokenFile"`
		DeployKeyPath string `json:"deployKeyPath"`
		// GitHub App: mint short-lived, auto-rotating per-installation tokens
		// instead of a long-lived PAT. All three are required together and take
		// precedence over Token/TokenFile.
		GitHubAppID             string `json:"githubAppId"`
		GitHubInstallationID    string `json:"githubInstallationId"`
		GitHubAppPrivateKeyFile string `json:"githubAppPrivateKeyFile"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse watch config: %w", err)
	}
	specs := make([]reconcile.RepoSpec, 0, len(raw))
	for _, r := range raw {
		auth := git.Auth{Token: r.Token, DeployKeyPath: r.DeployKeyPath}
		if r.TokenFile != "" {
			b, err := os.ReadFile(r.TokenFile)
			if err != nil {
				return nil, fmt.Errorf("watch repo %q: read tokenFile: %w", r.Name, err)
			}
			auth.Token = strings.TrimSpace(string(b))
		}
		if r.GitHubAppID != "" || r.GitHubInstallationID != "" || r.GitHubAppPrivateKeyFile != "" {
			if r.GitHubAppID == "" || r.GitHubInstallationID == "" || r.GitHubAppPrivateKeyFile == "" {
				return nil, fmt.Errorf("watch repo %q: githubAppId, githubInstallationId, and githubAppPrivateKeyFile are required together", r.Name)
			}
			pem, err := os.ReadFile(r.GitHubAppPrivateKeyFile)
			if err != nil {
				return nil, fmt.Errorf("watch repo %q: read githubAppPrivateKeyFile: %w", r.Name, err)
			}
			app, err := git.NewGitHubApp(r.GitHubAppID, r.GitHubInstallationID, pem)
			if err != nil {
				return nil, fmt.Errorf("watch repo %q: github app: %w", r.Name, err)
			}
			auth.TokenSource = app.Token
		}
		specs = append(specs, reconcile.RepoSpec{
			Name:      r.Name,
			URL:       r.URL,
			Ref:       config.RepoRef{Branch: r.Branch, Path: r.Path},
			Auth:      auth,
			Initiator: rollout.Identity{Kind: "ci", Name: "reconciler"},
		})
	}
	return specs, nil
}

// sessionCookie carries the opaque auth-go session token after a successful
// basic-auth handshake, so the SPA's same-origin fetch() calls authenticate
// without the browser replaying basic-auth credentials (which it does not do
// for fetch when the credentials were supplied via the document URL).
const sessionCookie = "rollops_ui"

// uiSessionTTL bounds how long a UI session stays valid before re-auth.
const uiSessionTTL = 12 * time.Hour

// basicAuth gates the browser dashboard. Credentials come from
// ROLLOPS_UI_USER / ROLLOPS_UI_PASSWORD; if the password is unset the UI is
// refused entirely rather than served anonymously. On a successful basic-auth
// hit it issues an auth-go session (server-side, with entropy + expiry
// invariants owned by the library) and sets its opaque token as an HttpOnly
// cookie; subsequent requests authenticate by validating that cookie, so the
// JSON API stays reachable from the SPA without re-sending credentials.
func uiAuth(next http.Handler, oidc api.Authenticator) http.Handler {
	if oidc == nil {
		return basicAuth(next)
	}
	fallback := basicAuth(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := oidc.Identify(bearerToken(r)); ok && id.Name != "" {
			next.ServeHTTP(w, r)
			return
		}
		fallback.ServeHTTP(w, r)
	})
}

func basicAuth(next http.Handler) http.Handler {
	user := envOr("ROLLOPS_UI_USER", "admin")
	pass := os.Getenv("ROLLOPS_UI_PASSWORD")
	// auth-go owns session issuance/validation; in-memory repo => sessions reset
	// on restart, which is the right behaviour for a single-node daemon.
	sessions := domain.NewSessionService(memory.NewSessionRepo(), uiSessionTTL, domain.SystemClock)
	uid, _ := domain.NewUserID(user)
	tid, _ := domain.NewTenantID("rollops")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if pass == "" {
			http.Error(w, "ui disabled: set ROLLOPS_UI_PASSWORD", http.StatusForbidden)
			return
		}
		// Cookie path: the SPA's fetch() rides on the issued session token.
		if c, err := r.Cookie(sessionCookie); err == nil {
			if tok, terr := domain.TokenFromString(c.Value); terr == nil {
				if _, verr := sessions.Validate(tok); verr == nil {
					next.ServeHTTP(w, r)
					return
				}
			}
		}
		u, p, ok := r.BasicAuth()
		if !ok || !subtleEqual(u, user) || !subtleEqual(p, pass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="rollops"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		sess, err := sessions.Issue(uid, tid)
		if err != nil {
			http.Error(w, "session issue failed", http.StatusInternalServerError)
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookie,
			Value:    sess.Token().String(),
			Path:     "/ui",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		next.ServeHTTP(w, r)
	})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	return ""
}

func subtleEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// buildPolicy assembles the RBAC policy from bootstrap defaults, an optional
// ROLLOPS_POLICY_FILE, and (when OIDC is enabled) the OIDC group→role binds.
// Used at startup and on every SIGHUP reload.
func buildPolicy(oidcOn bool) (*security.Policy, error) {
	policy := security.DefaultRBACPolicy()
	if pf := os.Getenv("ROLLOPS_POLICY_FILE"); pf != "" {
		if err := security.LoadPolicyFile(policy, pf); err != nil {
			return nil, err
		}
	}
	if oidcOn {
		if group := envOr("ROLLOPS_OIDC_ADMIN_GROUP", "rollops-admins"); group != "" {
			policy.Bind("group:"+group, security.RoleAdmin)
		}
		if group := os.Getenv("ROLLOPS_OIDC_AGENT_GROUP"); group != "" {
			policy.Bind("group:"+group, security.RoleAgent)
		}
	}
	return policy, nil
}

func buildOIDCAuth() api.Authenticator {
	issuer := os.Getenv("ROLLOPS_OIDC_ISSUER")
	audience := os.Getenv("ROLLOPS_OIDC_AUDIENCE")
	if issuer == "" || audience == "" {
		return nil
	}
	cfg := api.OIDCConfig{Issuer: issuer, Audience: audience}
	// Asymmetric (real IdP) verification: an explicit JWKS URL, or discover it
	// from the issuer's OIDC well-known document. Falls back to a shared HS256
	// secret when no JWKS is configured.
	jwksURL := os.Getenv("ROLLOPS_OIDC_JWKS_URL")
	if jwksURL == "" && os.Getenv("ROLLOPS_OIDC_DISCOVER") != "" {
		if u, err := api.DiscoverJWKSURL(nil, issuer); err == nil {
			jwksURL = u
		} else {
			fmt.Fprintf(os.Stderr, "rollopsd: OIDC discovery failed: %v\n", err)
		}
	}
	switch {
	case jwksURL != "":
		cfg.Keys = api.NewJWKS(jwksURL)
	case os.Getenv("ROLLOPS_OIDC_HS256_SECRET") != "":
		cfg.HMACSecret = os.Getenv("ROLLOPS_OIDC_HS256_SECRET")
	default:
		fmt.Fprintf(os.Stderr, "rollopsd: OIDC issuer/audience set but no JWKS URL or HS256 secret — OIDC disabled\n")
		return nil
	}
	return api.OIDCAuth{Config: cfg}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
