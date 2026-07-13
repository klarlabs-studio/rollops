package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"go.klarlabs.de/rollops/internal/rollout"
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

// TestPlan_ReferencedSource_ChecksumsRenderedOutput proves decision 3: when the
// target resolves a referenced source, the drift checksum keys off the RENDERED
// bytes (so an edit to a referenced file is detected even under shallow
// verification), and the rendered bytes are surfaced on the plan for preview.
func TestPlan_ReferencedSource_ChecksumsRenderedOutput(t *testing.T) {
	rendered := []byte("kind: Deployment\nmetadata: {name: rendered}\n")
	fake := &fakeTarget{referenced: true, rendered: rendered}
	e, _ := newEngine(t, fake)

	p, err := e.Plan(context.Background(), loadConfig(t))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	sum := sha256.Sum256(rendered)
	want := hex.EncodeToString(sum[:])
	if p.Desired.Checksum != want {
		t.Errorf("referenced checksum = %q, want sha256(rendered) %q", p.Desired.Checksum, want)
	}
	if string(p.Rendered) != string(rendered) {
		t.Errorf("plan must carry the rendered preview, got %q", p.Rendered)
	}
}

// TestPlan_InlineSource_KeepsSpecChecksum proves the inline path is unchanged:
// a non-referenced target keeps its spec-derived checksum and no preview.
func TestPlan_InlineSource_KeepsSpecChecksum(t *testing.T) {
	fake := &fakeTarget{} // referenced defaults to false
	e, _ := newEngine(t, fake)

	specSum, err := manifestFromConfig(loadConfig(t), "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := e.Plan(context.Background(), loadConfig(t))
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if p.Desired.Checksum != specSum.Checksum {
		t.Errorf("inline checksum = %q, want spec checksum %q", p.Desired.Checksum, specSum.Checksum)
	}
	if p.Rendered != nil {
		t.Errorf("inline plan must have no rendered preview, got %q", p.Rendered)
	}
}

// TestApply_ReferencedSource_StampsRenderedChecksum proves Apply stamps the
// rendered checksum (the annotation the target records) before applying.
func TestApply_ReferencedSource_StampsRenderedChecksum(t *testing.T) {
	rendered := []byte("kind: Service\nmetadata: {name: rendered}\n")
	fake := &fakeTarget{referenced: true, rendered: rendered}
	e, _ := newEngine(t, fake)

	_, err := e.Apply(context.Background(), ApplyRequest{Config: loadConfig(t), Initiator: rollout.Identity{Kind: "human", Name: "x"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	sum := sha256.Sum256(rendered)
	want := hex.EncodeToString(sum[:])
	if len(fake.applied) != 1 || fake.applied[0].Checksum != want {
		t.Errorf("applied checksum = %q, want sha256(rendered) %q", fake.applied[0].Checksum, want)
	}
}
