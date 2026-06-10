package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"go.klarlabs.de/rollops/internal/api"
	"go.klarlabs.de/rollops/internal/rollout"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}

// The browser supplies basic-auth credentials only for the document load (often
// via URL userinfo), which the browser does NOT replay on the SPA's fetch()
// calls. The gate therefore mints a session cookie on a successful basic-auth
// hit and accepts that cookie thereafter, so the API stays reachable.
func TestAuthGate_CookieSession(t *testing.T) {
	t.Setenv("ROLLOPS_UI_USER", "admin")
	t.Setenv("ROLLOPS_UI_PASSWORD", "dev")
	t.Setenv("ROLLOPS_UI_SECRET", "test-secret-stable")
	h := basicAuth(okHandler())

	// 1. No credentials → 401 challenge.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/ui", nil))
	if rr.Code != http.StatusUnauthorized || rr.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("no-auth = %d, want 401 challenge", rr.Code)
	}

	// 2. Valid basic auth → 200 and a session cookie is minted.
	req := httptest.NewRequest("GET", "/ui", nil)
	req.SetBasicAuth("admin", "dev")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("basic auth = %d, want 200", rr.Code)
	}
	cookies := rr.Result().Cookies()
	var session *http.Cookie
	for _, c := range cookies {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil || session.Value == "" {
		t.Fatalf("no %s cookie minted: %+v", sessionCookie, cookies)
	}
	if !session.HttpOnly {
		t.Error("session cookie must be HttpOnly")
	}

	// 3. Cookie alone (no basic auth) → 200, simulating the SPA's fetch().
	req = httptest.NewRequest("GET", "/ui/api/dashboard", nil)
	req.AddCookie(session)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("cookie-only = %d, want 200", rr.Code)
	}

	// 4. Forged cookie → 401.
	req = httptest.NewRequest("GET", "/ui/api/dashboard", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "forged"})
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("forged cookie = %d, want 401", rr.Code)
	}
}

func TestAuthGate_DisabledWithoutPassword(t *testing.T) {
	t.Setenv("ROLLOPS_UI_PASSWORD", "")
	rr := httptest.NewRecorder()
	basicAuth(okHandler()).ServeHTTP(rr, httptest.NewRequest("GET", "/ui", nil))
	if rr.Code != http.StatusForbidden {
		t.Errorf("no password = %d, want 403", rr.Code)
	}
}

func TestUIAuth_AcceptsBearerAuthenticator(t *testing.T) {
	t.Setenv("ROLLOPS_UI_PASSWORD", "")
	h := uiAuth(okHandler(), api.TokenAuth{"oidc-token": rollout.Identity{Kind: "human", Name: "ada"}})
	req := httptest.NewRequest("GET", "/ui", nil)
	req.Header.Set("Authorization", "Bearer oidc-token")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("oidc bearer = %d, want 200", rr.Code)
	}
}
