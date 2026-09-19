package release

import (
	"testing"

	"go.klarlabs.de/rollops/internal/domain/provenance"
)

// A fingerprint is a stored fact: FindByFingerprint answers "have we already
// built this" by comparing against values written by earlier binaries. Changing
// how one is derived silently re-answers that question as "no" for everything
// already on disk, so the expected digest is pinned here rather than recomputed
// by the test.
//
// If this fails, the encoding changed. That may be intended — but it is a
// migration, not a refactor.
func TestTheFingerprintEncodingIsPinned(t *testing.T) {
	r := Release{
		ProjectID: "prj_1",
		Version:   "1.2.3",
		Source: provenance.SourceRevision{
			Provider:   "git",
			Repository: "klarlabs/rollops",
			Revision:   "5f2a1c9e7b3d4a6f8e0c2b4d6a8f0c2e4b6d8a0f",
		},
		Artifacts: []Artifact{
			{ArtifactID: "art_2", Role: "b"},
			{ArtifactID: "art_1", Role: "a"},
		},
	}
	const want = "sha256:8b41cfd0aa9f1996aba1bbac369c86ce5cb84a67c9415b82019cbbae276e3f06"
	if got := r.Fingerprint().String(); got != want {
		t.Errorf("Fingerprint() = %s, want %s", got, want)
	}
}
