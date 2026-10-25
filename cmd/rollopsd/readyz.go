package main

import (
	"context"
	"net/http"
	"time"
)

// readyzTimeout bounds a readiness check so a hung database answers 503
// instead of holding probe connections open.
const readyzTimeout = 900 * time.Millisecond

// readyzHandler answers 200 only when the store is readable. It used to
// answer 200 unconditionally, which kept a pod with a corrupt database in
// service for every repo it watched (#196).
func readyzHandler(ready func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readyzTimeout)
		defer cancel()
		if err := ready(ctx); err != nil {
			http.Error(w, "store not ready: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}
