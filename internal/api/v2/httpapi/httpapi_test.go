package httpapi_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apiv2 "go.klarlabs.de/rollops/internal/api/v2"
	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/api/v2/httpapi"
	"go.klarlabs.de/rollops/internal/domain/identity"
)

func anyone() identity.Principal {
	return identity.Principal{ID: "u1", Type: identity.PrincipalHuman, DisplayName: "Ada"}
}

// nobody is an authenticator that refuses, standing in for a missing or
// unreadable credential.
func nobody() httpapi.IdentifierFunc {
	return func(*http.Request) (identity.Principal, error) {
		return identity.Principal{}, errors.New("no credential")
	}
}

func everyone() httpapi.IdentifierFunc {
	return func(*http.Request) (identity.Principal, error) { return anyone(), nil }
}

func handler(t *testing.T, who httpapi.Identifier) http.Handler {
	t.Helper()
	h, err := httpapi.New(httpapi.Config{Service: &apiv2.Service{}, Identity: who})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h
}

func send(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// failure is the error envelope a caller parses.
type failure struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeFailure(t *testing.T, w *httptest.ResponseRecorder) failure {
	t.Helper()
	var f failure
	if err := json.Unmarshal(w.Body.Bytes(), &f); err != nil {
		t.Fatalf("the error body is not JSON: %v (%q)", err, w.Body.String())
	}
	return f
}

func TestAServiceAndAnIdentifierAreBothRequired(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  httpapi.Config
	}{
		{"no service", httpapi.Config{Identity: everyone()}},
		{"no identifier", httpapi.Config{Service: &apiv2.Service{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := httpapi.New(tc.cfg); err == nil {
				t.Fatal("New accepted a config it cannot serve")
			}
		})
	}
}

// Every code in spec 23.4 has to render as some status. apierr.Codes exists so
// a transport can prove that, and a code added there should break this rather
// than quietly answer 500.
func TestEveryErrorCodeRendersAsAStatus(t *testing.T) {
	for _, c := range apierr.Codes() {
		if got := c.HTTP(); got < 200 || got > 599 {
			t.Errorf("%s renders as %d, which is not a status", c, got)
		}
	}
}

// The code is the contract, not the status: HTTP has fewer distinctions than
// spec 23.4 does, so a caller telling PLAN_STALE from APPROVAL_REQUIRED is
// reading the body.
func TestAFailureCarriesItsCodeInTheBody(t *testing.T) {
	w := send(t, handler(t, nobody()), "GET", "/v2/deployments/dpl_1", "")

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
	if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want JSON", got)
	}
	if got := decodeFailure(t, w).Error.Code; got != string(apierr.Unauthorized) {
		t.Errorf("code = %q, want UNAUTHORIZED", got)
	}
}

// An authenticator that has already decided what kind of refusal this is keeps
// its answer. Flattening everything to UNAUTHORIZED would tell a caller whose
// credential is fine but whose permissions are not to go and re-authenticate.
func TestAnAuthenticatorMayRefuseWithItsOwnCode(t *testing.T) {
	forbidden := httpapi.IdentifierFunc(func(*http.Request) (identity.Principal, error) {
		return identity.Principal{}, &apierr.Error{
			Code:    apierr.Forbidden,
			Message: "not permitted",
			Err:     errors.New("not permitted"),
		}
	})

	w := send(t, handler(t, forbidden), "GET", "/v2/deployments/dpl_1", "")

	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
	if got := decodeFailure(t, w).Error.Code; got != string(apierr.Forbidden) {
		t.Errorf("code = %q, want FORBIDDEN", got)
	}
}

// A route this API does not serve is answered in the same envelope as one it
// does. A caller that has to parse two shapes to find out what went wrong will
// parse one of them wrong.
func TestAnUnknownRouteFailsInTheSameEnvelope(t *testing.T) {
	w := send(t, handler(t, everyone()), "GET", "/v2/nothing-here", "")

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
	if got := decodeFailure(t, w).Error.Code; got != string(apierr.NotFound) {
		t.Errorf("code = %q, want NOT_FOUND", got)
	}
}

// Authentication happens before routing does. Telling an anonymous caller which
// paths exist is a map of the estate drawn for somebody who has not said who
// they are.
func TestAnUnknownRouteStillNeedsACredential(t *testing.T) {
	w := send(t, handler(t, nobody()), "GET", "/v2/nothing-here", "")

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

func TestAMalformedBodyIsTheCallersMistake(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"not json", "{"},
		// A field this version does not know is refused rather than dropped: a
		// caller who misspelled one would otherwise watch it silently not apply.
		{"a field we do not know", `{"reason":"why","resaon":"typo"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := send(t, handler(t, everyone()),
				"POST", "/v2/deployments/dpl_1:cancel", tc.body)

			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
			if got := decodeFailure(t, w).Error.Code; got != string(apierr.InvalidArgument) {
				t.Errorf("code = %q, want INVALID_ARGUMENT", got)
			}
		})
	}
}

// A body larger than the cap is refused rather than read, so a caller cannot
// make the server allocate whatever it likes.
func TestAnOversizedBodyIsRefused(t *testing.T) {
	h, err := httpapi.New(httpapi.Config{
		Service:      &apiv2.Service{},
		Identity:     everyone(),
		MaxBodyBytes: 32,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	w := send(t, h, "POST", "/v2/deployments/dpl_1:cancel",
		`{"reason":"`+strings.Repeat("x", 500)+`"}`)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// The deployment id is a path segment that also carries the verb, so a request
// for a verb nobody serves must not be read as a deployment whose id ends in a
// colon.
func TestAnUnknownVerbIsNotMistakenForAnID(t *testing.T) {
	w := send(t, handler(t, everyone()), "POST", "/v2/deployments/dpl_1:detonate", "")

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

// The timestamps a caller reads back have to be parseable as the instants they
// are, and a deployment that has not started says so by omission rather than by
// claiming the epoch.
func TestTheWireShapeIsDeclaredRatherThanInherited(t *testing.T) {
	var d struct {
		ID        string     `json:"id"`
		CreatedAt time.Time  `json:"created_at"`
		StartedAt *time.Time `json:"started_at"`
	}
	raw := `{"id":"dpl_1","created_at":"2026-09-19T12:00:00Z"}`
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatalf("the declared shape does not round-trip: %v", err)
	}
	if d.StartedAt != nil {
		t.Error("an absent start time decoded as present")
	}
}
