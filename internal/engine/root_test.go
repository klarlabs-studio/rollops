package engine

import (
	"context"
	"testing"
)

// TestManifestFromConfig_RootThreadedNotChecksummed proves Root is carried on
// the manifest but excluded from the drift checksum — the same config yields the
// same identity regardless of which checkout dir it renders from.
func TestManifestFromConfig_RootThreadedNotChecksummed(t *testing.T) {
	c := loadConfig(t)

	a, err := manifestFromConfig(c, "/checkout/a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := manifestFromConfig(c, "/checkout/b")
	if err != nil {
		t.Fatal(err)
	}
	if a.Root != "/checkout/a" {
		t.Errorf("Root = %q, want /checkout/a", a.Root)
	}
	if a.Checksum != b.Checksum {
		t.Errorf("checksum must not depend on Root: %q != %q", a.Checksum, b.Checksum)
	}
	if a.Checksum == "" {
		t.Error("checksum must be set")
	}
}

// TestWithRoot_RoundTrip proves the context carrier used to thread the root
// through Plan (whose signature is fixed by the Operations boundary).
func TestWithRoot_RoundTrip(t *testing.T) {
	ctx := WithRoot(context.Background(), "/checkout/x")
	if got := rootFromContext(ctx); got != "/checkout/x" {
		t.Errorf("rootFromContext = %q, want /checkout/x", got)
	}
	// Empty root must not shadow — it stays absent so callers fall through.
	if got := rootFromContext(WithRoot(context.Background(), "")); got != "" {
		t.Errorf("empty root should not be carried, got %q", got)
	}
}
