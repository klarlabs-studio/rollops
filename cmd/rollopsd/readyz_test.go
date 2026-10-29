package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestReadyz_ReadyStoreAnswers200(t *testing.T) {
	rr := httptest.NewRecorder()
	readyzHandler(func(context.Context) error { return nil })(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200", rr.Code)
	}
}

// #196: a corrupt store kept /readyz at 200 while every apply failed.
func TestReadyz_UnreadableStoreAnswers503WithTheReason(t *testing.T) {
	rr := httptest.NewRecorder()
	readyzHandler(func(context.Context) error {
		return errors.New("sqlite: quick_check: *** in database main ***")
	})(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "quick_check") {
		t.Fatalf("body %q does not say why", rr.Body.String())
	}
}

func TestReadyz_HungStoreAnswers503WithinTheTimeout(t *testing.T) {
	rr := httptest.NewRecorder()
	start := time.Now()
	readyzHandler(func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("code = %d, want 503", rr.Code)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("took %s; the kubelet probe times out after 1s and must not pile up", elapsed)
	}
}
