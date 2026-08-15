package mcp

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	mcpserver "go.klarlabs.de/mcp"

	"go.klarlabs.de/rollops/internal/api"
)

// testAuth is the token→identity map both surfaces share (api.TokenAuth).
var testAuth = api.TokenAuth{
	"tok-nomi": {Kind: "agent", Name: "nomi"},
	"tok-bot":  {Kind: "agent", Name: "deploy-bot"},
}

func bearerReq(t *testing.T, token string) *http.Request {
	t.Helper()
	r := httptestRequest()
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r
}

// httptestRequest builds a minimal POST request; only headers matter to the hooks.
func httptestRequest() *http.Request {
	r, _ := http.NewRequest(http.MethodPost, "http://mcp.local/mcp", nil)
	return r
}

// TestAuthorize_FailClosed covers the WithAuthorize gate: only a token that
// resolves to an identity is accepted; missing, empty, and unknown tokens are
// rejected before any handler runs.
func TestAuthorize_FailClosed(t *testing.T) {
	gate := authorize(testAuth)
	cases := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{"valid token", "tok-nomi", false},
		{"second valid token", "tok-bot", false},
		{"unknown token", "tok-unknown", true},
		{"empty token", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := gate(bearerReq(t, tc.token))
			if tc.wantErr && err == nil {
				t.Fatalf("token %q: want rejection, got nil", tc.token)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("token %q: want accept, got %v", tc.token, err)
			}
		})
	}
}

// TestInjectIdentity_PerCaller covers WithRequestContextFn: a resolvable token
// puts THAT caller's identity into the handler context; distinct tokens yield
// distinct identities; an unresolved token leaves the context without one.
func TestInjectIdentity_PerCaller(t *testing.T) {
	inject := injectIdentity(testAuth)
	cases := []struct {
		name     string
		token    string
		wantName string
		wantOK   bool
	}{
		{"nomi token resolves to nomi", "tok-nomi", "nomi", true},
		{"bot token resolves to deploy-bot", "tok-bot", "deploy-bot", true},
		{"unknown token injects nothing", "tok-unknown", "", false},
		{"no token injects nothing", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := inject(context.Background(), bearerReq(t, tc.token))
			id, ok := identityFrom(ctx)
			if ok != tc.wantOK {
				t.Fatalf("token %q: identity present = %v, want %v", tc.token, ok, tc.wantOK)
			}
			if id.Name != tc.wantName {
				t.Fatalf("token %q: identity name = %q, want %q", tc.token, id.Name, tc.wantName)
			}
		})
	}
}

func TestAuthServeOptions_ReturnsBothHooks(t *testing.T) {
	if got := len(AuthServeOptions(testAuth)); got != 2 {
		t.Fatalf("AuthServeOptions returned %d options, want 2 (authorize + request-context)", got)
	}
}

// --- end-to-end: a bearer header must reach a tool handler with the right
// identity in ctx, via mcp-go's real HTTP serve path ---

type probeInput struct{}

type probeOutput struct {
	Caller string `json:"caller"`
	Authed bool   `json:"authed"`
}

// TestServeHTTP_BearerReachesHandlerIdentity spins up the real mcp-go HTTP
// transport with AuthServeOptions and a probe tool that echoes the identity it
// sees in ctx. It proves end-to-end that (a) a valid bearer propagates the mapped
// identity into the handler, (b) two tokens produce two distinct identities, and
// (c) a request with no token is rejected (403) before the handler runs.
func TestServeHTTP_BearerReachesHandlerIdentity(t *testing.T) {
	// A probe tool that reports the caller the transport injected into ctx.
	srv := mcpserver.NewServer(mcpserver.ServerInfo{Name: "rollops-test", Version: "0.0.0", Capabilities: mcpserver.Capabilities{Tools: true}})
	var probeCalls int
	srv.Tool("probe").Description("echo caller identity").Handler(func(ctx context.Context, _ probeInput) (probeOutput, error) {
		probeCalls++
		id, ok := identityFrom(ctx)
		return probeOutput{Caller: id.Name, Authed: ok}, nil
	})

	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = mcpserver.ServeHTTP(ctx, srv, addr, AuthServeOptions(testAuth)...) }()
	waitReady(t, addr)

	// Valid token → handler runs and sees nomi.
	status, body := postProbe(t, addr, "tok-nomi")
	if status != http.StatusOK {
		t.Fatalf("valid token: status = %d, body = %s", status, body)
	}
	if !strings.Contains(body, "nomi") {
		t.Errorf("valid token: response missing caller identity, body = %s", body)
	}

	// A different token → a different identity (per-caller propagation).
	status, body = postProbe(t, addr, "tok-bot")
	if status != http.StatusOK {
		t.Fatalf("second token: status = %d, body = %s", status, body)
	}
	if !strings.Contains(body, "deploy-bot") {
		t.Errorf("second token: response missing caller identity, body = %s", body)
	}
	if strings.Contains(body, `"caller":"nomi"`) {
		t.Errorf("second token leaked first caller's identity: %s", body)
	}

	callsBefore := probeCalls

	// No token → rejected at the transport (fail-closed), handler never reached.
	status, body = postProbe(t, addr, "")
	if status != http.StatusForbidden {
		t.Fatalf("no token: status = %d, want 403; body = %s", status, body)
	}
	// Unknown token → likewise rejected.
	status, body = postProbe(t, addr, "tok-unknown")
	if status != http.StatusForbidden {
		t.Fatalf("unknown token: status = %d, want 403; body = %s", status, body)
	}
	if probeCalls != callsBefore {
		t.Errorf("handler ran %d extra time(s) for rejected requests; want 0 (fail-closed)", probeCalls-callsBefore)
	}
}

// freeAddr reserves an ephemeral localhost port and returns it for the server to
// bind. Closing the probe listener first avoids requiring the transport to
// expose its bound address.
func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

func waitReady(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("mcp server on %s never became ready", addr)
}

// postProbe POSTs a tools/call for the probe tool, with an optional bearer token.
// mcp v1.26+ ServeHTTP defaults to Streamable HTTP (stateless): Mcp-Method is
// required on POST /mcp.
func postProbe(t *testing.T, addr, token string) (int, string) {
	t.Helper()
	const body = `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"probe","arguments":{}}}`
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Method", "tools/call")
	req.Header.Set("MCP-Protocol-Version", "2025-11-25")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}
