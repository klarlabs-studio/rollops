// How a request becomes a value and a value becomes a response.
//
// Everything a caller is told about a failure goes through fail, and everything
// it is told about a success goes through respond. Both build the whole body
// before writing a status, so a value that fails to encode halfway through
// cannot leave a truncated 200 behind.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"go.klarlabs.de/rollops/internal/api/v2/apierr"
	"go.klarlabs.de/rollops/internal/api/v2/page"
)

// serve authenticates, runs fn, and renders whatever it returns.
//
// Authentication happens before the handler, which means before routing has
// decided anything: telling an anonymous caller which paths exist is a map of
// the estate drawn for somebody who has not said who they are.
func (h *api) serve(fn handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, err := h.who.Identify(r)
		if err != nil {
			fail(w, refusal(err))
			return
		}
		if err := fn(w, r, who); err != nil {
			fail(w, apierr.From(err))
		}
	})
}

// refusal classifies what an Identifier returned. One that has already decided
// what kind of refusal this is keeps its answer: flattening everything to
// UNAUTHORIZED would tell a caller whose credential is fine but whose
// permissions are not to go and re-authenticate.
func refusal(err error) *apierr.Error {
	var already *apierr.Error
	if errors.As(err, &already) {
		return already
	}
	return &apierr.Error{
		Code:    apierr.Unauthorized,
		Message: "the caller was not identified",
		Err:     err,
	}
}

func noSuchRoute() error {
	return &apierr.Error{
		Code:    apierr.NotFound,
		Message: "no such endpoint",
		Err:     errors.New("httpapi: no such endpoint"),
	}
}

func badRequest(err error) error {
	return &apierr.Error{Code: apierr.InvalidArgument, Message: err.Error(), Err: err}
}

// failure is the error envelope. It is one object rather than a bare code and
// message so that a success body and a failure body can never be mistaken for
// one another by a caller that only looks at the top-level keys.
type failure struct {
	Error failureDetail `json:"error"`
}

type failureDetail struct {
	// Code is the contract, not the status. HTTP has fewer distinctions than
	// §23.4 does, so a caller telling PLAN_STALE from APPROVAL_REQUIRED — both
	// 409 — is reading this field.
	Code string `json:"code"`

	Message string `json:"message"`
}

func fail(w http.ResponseWriter, e *apierr.Error) {
	respond(w, e.Code.HTTP(), failure{failureDetail{
		Code:    string(e.Code),
		Message: e.Message,
	}})
}

func respond(w http.ResponseWriter, status int, body any) {
	encoded, err := json.Marshal(body)
	if err != nil {
		// Our own view did not encode. Nothing the caller did caused it and
		// nothing they can change will fix it, so it is INTERNAL and the detail
		// stays here rather than going out (INV-012).
		write(w, http.StatusInternalServerError,
			[]byte(`{"error":{"code":"INTERNAL","message":"the response could not be encoded"}}`))
		return
	}
	write(w, status, encoded)
}

func write(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// decode reads the request body into v.
//
// An absent body is an empty object rather than an error: several commands
// have nothing required in theirs, and what a request must carry is the
// service's to decide, not the decoder's. Unknown fields are refused rather
// than dropped — a caller who misspelled one would otherwise watch it silently
// not apply.
func (h *api) decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, h.maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	switch err := dec.Decode(v); {
	case errors.Is(err, io.EOF):
		return nil
	case err != nil:
		return badRequest(err)
	}
	// A body carrying a second value is not a body this API was sent. Reading
	// only the first would quietly act on half of what a caller meant.
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return badRequest(errors.New("the request body carries more than one value"))
	}
	return nil
}

// idempotencyKey is how a retry says it is a retry (§18.3). It is a header
// rather than a body field so that it reads the same on every command,
// including the ones whose bodies are empty.
func idempotencyKey(r *http.Request) string {
	return r.Header.Get("Idempotency-Key")
}

// pageOf reads the cursor and size a list call was asked for. Both are
// optional, and a size that is not a number is the caller's mistake rather
// than a silent fall back to the default — a client sending page_size=fifty
// wants fifty of something and should be told it asked wrongly.
func pageOf(r *http.Request) (page.Request, error) {
	q := r.URL.Query()
	req := page.Request{Cursor: q.Get("page_token")}
	if raw := q.Get("page_size"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return page.Request{}, badRequest(errors.New("page_size is not a number"))
		}
		req.Size = n
	}
	return req, nil
}
