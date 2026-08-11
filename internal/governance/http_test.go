package governance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The gate exists to stop a deploy that governance refuses. Everything below is
// about the ways it could fail to do that — by allowing on error, by allowing when
// the governor is unreachable, or by not asking at all.

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

// hookFor builds the hook under test from environment variables, the way a daemon
// would.
func hookFor(vars map[string]string) Hook {
	return Hook{Provider: FromEnv(env(vars))}
}

func applyRequest() Request {
	return Request{Action: "apply", TargetRef: "k8s/prod/api", Environment: "prod", Version: "1.4.0"}
}

// A user who has not asked for external governance must be entirely unaffected by
// this existing. No URL means no provider, and a Hook with no provider allows.
func TestFromEnvIsNilWithoutAURL(t *testing.T) {
	if p := FromEnv(env(nil)); p != nil {
		t.Fatalf("FromEnv returned %#v with no URL set; unconfigured must mean no gate", p)
	}
}

func TestAllowedDecisionPassesThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(wireDecision{
			Allowed:  true,
			Evidence: map[string]string{"release_id": "run-7"},
		})
	}))
	defer srv.Close()

	d, err := hookFor(map[string]string{"ROLLOPS_GOVERNANCE_URL": srv.URL}).
		Evaluate(context.Background(), applyRequest())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if !d.Allowed {
		t.Error("an allowing governor must allow")
	}
	if d.Evidence["release_id"] != "run-7" {
		t.Errorf("Evidence = %v; the governor's evidence must survive, it is what the audit "+
			"entry records", d.Evidence)
	}
}

func TestDeniedDecisionCarriesItsReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(wireDecision{Allowed: false, Reason: "no approval on record"})
	}))
	defer srv.Close()

	d, err := hookFor(map[string]string{"ROLLOPS_GOVERNANCE_URL": srv.URL}).
		Evaluate(context.Background(), applyRequest())
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if d.Allowed {
		t.Fatal("a refusing governor must refuse")
	}
	if d.Reason != "no approval on record" {
		t.Errorf("Reason = %q, want the governor's own words — an operator reading "+
			"'denied by governance hook' cannot tell what to fix", d.Reason)
	}
}

// The decision this gate turns on. A configured governor that cannot be reached is
// not the same as no governor configured: giving them the same outcome would make
// the gate disappear exactly when a rushed deploy is most likely.
func TestAnUnreachableGovernorDenies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now

	_, err := hookFor(map[string]string{"ROLLOPS_GOVERNANCE_URL": url}).
		Evaluate(context.Background(), applyRequest())
	if err == nil {
		t.Fatal("an unreachable governor must produce an error, which the engine treats as " +
			"a block; allowing here would mean a bad network silently removes the gate")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Errorf("error = %q, want it to say the governor was unreachable so an operator "+
			"can tell an outage from a refusal", err)
	}
}

// A governor answering 500 has not decided anything. Treating a broken governor as
// permission is the same failure as treating an unreachable one as permission.
func TestAGovernorErrorDenies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := hookFor(map[string]string{"ROLLOPS_GOVERNANCE_URL": srv.URL}).
		Evaluate(context.Background(), applyRequest()); err == nil {
		t.Fatal("a 500 from the governor must not read as permission")
	}
}

func TestAnUnreadableDecisionDenies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	defer srv.Close()

	if _, err := hookFor(map[string]string{"ROLLOPS_GOVERNANCE_URL": srv.URL}).
		Evaluate(context.Background(), applyRequest()); err == nil {
		t.Fatal("an unparseable body is not a decision; a proxy returning an HTML error " +
			"page must not read as permission")
	}
}

// The governor needs to know what is going where. A request carrying only a target
// ref cannot be decided on: prod and staging deserve different answers, and an
// external system holding a release record needs the version to find it.
func TestTheRequestCarriesWhatIsGoingWhere(t *testing.T) {
	var got wireRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_ = json.NewEncoder(w).Encode(wireDecision{Allowed: true})
	}))
	defer srv.Close()

	req := applyRequest()
	req.Actor.Kind = "human"
	req.Actor.Name = "felix"
	if _, err := hookFor(map[string]string{"ROLLOPS_GOVERNANCE_URL": srv.URL}).
		Evaluate(context.Background(), req); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	for _, c := range []struct{ field, got, want string }{
		{"action", got.Action, "apply"},
		{"target_ref", got.TargetRef, "k8s/prod/api"},
		{"environment", got.Environment, "prod"},
		{"version", got.Version, "1.4.0"},
		{"actor_id", got.ActorID, "felix"},
		{"actor_kind", got.ActorKind, "human"},
	} {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q", c.field, c.got, c.want)
		}
	}
}

// The signature is what lets a governor trust that a request came from this engine
// rather than from anything that can reach its URL. Computed over the exact bytes
// sent, or the governor recomputes a different digest and rejects every request.
func TestTheRequestIsSignedWhenASecretIsSet(t *testing.T) {
	const secret = "shared-secret"
	var signature string
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		signature = r.Header.Get("X-Rollops-Signature")
		body, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(wireDecision{Allowed: true})
	}))
	defer srv.Close()

	if _, err := hookFor(map[string]string{
		"ROLLOPS_GOVERNANCE_URL":    srv.URL,
		"ROLLOPS_GOVERNANCE_SECRET": secret,
	}).Evaluate(context.Background(), applyRequest()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); signature != want {
		t.Errorf("signature = %q, want %q (HMAC-SHA256 over the exact bytes sent)", signature, want)
	}
}

func TestNoSignatureHeaderWithoutASecret(t *testing.T) {
	var present bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header["X-Rollops-Signature"]
		_ = json.NewEncoder(w).Encode(wireDecision{Allowed: true})
	}))
	defer srv.Close()

	if _, err := hookFor(map[string]string{"ROLLOPS_GOVERNANCE_URL": srv.URL}).
		Evaluate(context.Background(), applyRequest()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if present {
		t.Error("an unsigned setup must not send an empty signature header: a governor " +
			"cannot distinguish that from a forgery")
	}
}

// A governor that never answers must not hold the deploy open indefinitely. A
// stalled rollout is a worse failure than a refused one, because a refusal is
// legible and can be overridden.
func TestASlowGovernorTimesOutAndDenies(t *testing.T) {
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-blocked
	}))
	defer srv.Close()
	defer close(blocked)

	provider := FromEnv(env(map[string]string{
		"ROLLOPS_GOVERNANCE_URL":     srv.URL,
		"ROLLOPS_GOVERNANCE_TIMEOUT": "50ms",
	}))
	if got := provider.(*HTTPProvider).Timeout; got != 50*time.Millisecond {
		t.Fatalf("Timeout = %v, want the configured 50ms", got)
	}

	if _, err := (Hook{Provider: provider}).Evaluate(context.Background(), applyRequest()); err == nil {
		t.Fatal("a governor that never answers must produce an error rather than hang")
	}
}

// A mistyped duration keeps the default rather than failing construction: refusing
// to start would take the deploy path down over a formatting error.
func TestAnUnparseableTimeoutKeepsTheDefault(t *testing.T) {
	p := FromEnv(env(map[string]string{
		"ROLLOPS_GOVERNANCE_URL":     "https://governor.example",
		"ROLLOPS_GOVERNANCE_TIMEOUT": "five seconds",
	}))
	if got := p.(*HTTPProvider).Timeout; got != defaultGovernanceTimeout {
		t.Errorf("Timeout = %v, want the %v default", got, defaultGovernanceTimeout)
	}
}

// A governor behind ordinary API authentication needs a credential, and an HMAC
// signature is not one: it proves the body was not altered, not who is asking. A
// provider that could only sign would be turned away by every authenticated governor,
// which would quietly limit this to unauthenticated ones.
func TestTheRequestCarriesABearerTokenWhenSet(t *testing.T) {
	var authorization string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		_ = json.NewEncoder(w).Encode(wireDecision{Allowed: true})
	}))
	defer srv.Close()

	if _, err := hookFor(map[string]string{
		"ROLLOPS_GOVERNANCE_URL":   srv.URL,
		"ROLLOPS_GOVERNANCE_TOKEN": "tok-abc123",
	}).Evaluate(context.Background(), applyRequest()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if authorization != "Bearer tok-abc123" {
		t.Errorf("Authorization = %q, want %q", authorization, "Bearer tok-abc123")
	}
}

// A token and a signature answer different questions and must not exclude each other:
// a governor may reasonably want proof of who is asking *and* that the body is intact.
func TestATokenAndASignatureCoexist(t *testing.T) {
	var authorization, signature string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		signature = r.Header.Get("X-Rollops-Signature")
		_ = json.NewEncoder(w).Encode(wireDecision{Allowed: true})
	}))
	defer srv.Close()

	if _, err := hookFor(map[string]string{
		"ROLLOPS_GOVERNANCE_URL":    srv.URL,
		"ROLLOPS_GOVERNANCE_TOKEN":  "tok-abc123",
		"ROLLOPS_GOVERNANCE_SECRET": "shared-secret",
	}).Evaluate(context.Background(), applyRequest()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}

	if authorization == "" {
		t.Error("the bearer token was dropped when a signing secret was also configured")
	}
	if signature == "" {
		t.Error("the signature was dropped when a bearer token was also configured")
	}
}

func TestNoAuthorizationHeaderWithoutAToken(t *testing.T) {
	var present bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header["Authorization"]
		_ = json.NewEncoder(w).Encode(wireDecision{Allowed: true})
	}))
	defer srv.Close()

	if _, err := hookFor(map[string]string{"ROLLOPS_GOVERNANCE_URL": srv.URL}).
		Evaluate(context.Background(), applyRequest()); err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	if present {
		t.Error("an empty Authorization header was sent: a governor cannot distinguish " +
			"that from a malformed credential")
	}
}
