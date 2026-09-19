package artifact

import (
	"errors"
	"testing"
	"time"

	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/provenance"
)

var (
	dig = digest.Of([]byte("layer bytes"))
	at  = time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
)

func valid() Artifact {
	return Artifact{
		ID:        "art_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d001",
		ProjectID: "prj_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d002",
		Kind:      KindOCIImage,
		Digest:    dig,
		Locator:   "ghcr.io/klarlabs-studio/rollops@" + dig.String(),
		Size:      4096,
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		CreatedAt: at,
	}
}

func TestValidArtifact(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestArtifactRequiresItsIdentity(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Artifact)
	}{
		{"no id", func(a *Artifact) { a.ID = "" }},
		{"foreign id", func(a *Artifact) { a.ID = "rel_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d001" }},
		{"no project", func(a *Artifact) { a.ProjectID = "" }},
		{"foreign project", func(a *Artifact) { a.ProjectID = "env_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d002" }},
		{"unknown kind", func(a *Artifact) { a.Kind = Kind("interpretive dance") }},
		{"no kind", func(a *Artifact) { a.Kind = "" }},
		{"no digest", func(a *Artifact) { a.Digest = digest.Digest{} }},
		{"no locator", func(a *Artifact) { a.Locator = "" }},
		{"negative size", func(a *Artifact) { a.Size = -1 }},
		{"no creation time", func(a *Artifact) { a.CreatedAt = time.Time{} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := valid()
			c.mutate(&a)
			if err := a.Validate(); err == nil {
				t.Error("Validate() = nil, want an error")
			}
		})
	}
}

// INV-002: resolution happens once, at release creation. An OCI locator that
// still carries a tag would be re-resolved at deploy time and could hand a
// different image to production than the one that was verified.
func TestOCILocatorMustBeDigestPinned(t *testing.T) {
	cases := []struct {
		name    string
		locator string
		ok      bool
	}{
		{"pinned", "ghcr.io/x/y@" + dig.String(), true},
		{"pinned with a tag as well", "ghcr.io/x/y:v1.2.3@" + dig.String(), true},
		{"tag only", "ghcr.io/x/y:v1.2.3", false},
		{"latest", "ghcr.io/x/y:latest", false},
		{"bare repository", "ghcr.io/x/y", false},
		{"pinned to another digest", "ghcr.io/x/y@" + digest.Of([]byte("other")).String(), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := valid()
			a.Locator = c.locator
			err := a.Validate()
			if c.ok && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
			if !c.ok && !errors.Is(err, ErrUnpinnedLocator) {
				t.Errorf("Validate() = %v, want ErrUnpinnedLocator", err)
			}
		})
	}
}

// The rule is about registries that resolve a reference at pull time. A file
// path or URL is fetched and then checked against Digest, so it needs no pin.
func TestNonOCIKindsDoNotNeedAPinnedLocator(t *testing.T) {
	for _, k := range []Kind{KindBinary, KindArchive, KindManifestBundle, KindWASM, KindFile} {
		t.Run(string(k), func(t *testing.T) {
			a := valid()
			a.Kind = k
			a.Locator = "https://downloads.example.com/rollops-v1.2.3.tar.gz"
			if err := a.Validate(); err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestAttachedDocumentsMustMatchTheirSlot(t *testing.T) {
	sbom := provenance.DocumentRef{Kind: provenance.DocumentSBOM, Format: "spdx-json", Digest: dig}
	sig := provenance.DocumentRef{Kind: provenance.DocumentSignature, Format: "cosign-bundle", Digest: dig}
	prov := provenance.DocumentRef{Kind: provenance.DocumentProvenance, Format: "slsa-v1", Digest: dig}

	t.Run("correctly filed", func(t *testing.T) {
		a := valid()
		a.SBOMs = []provenance.DocumentRef{sbom}
		a.Signatures = []provenance.DocumentRef{sig}
		a.Provenance = prov
		if err := a.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil", err)
		}
	})
	t.Run("signature filed as an sbom", func(t *testing.T) {
		a := valid()
		a.SBOMs = []provenance.DocumentRef{sig}
		if err := a.Validate(); err == nil {
			t.Error("Validate() = nil, want an error")
		}
	})
	t.Run("sbom filed as a signature", func(t *testing.T) {
		a := valid()
		a.Signatures = []provenance.DocumentRef{sbom}
		if err := a.Validate(); err == nil {
			t.Error("Validate() = nil, want an error")
		}
	})
	t.Run("sbom filed as provenance", func(t *testing.T) {
		a := valid()
		a.Provenance = sbom
		if err := a.Validate(); err == nil {
			t.Error("Validate() = nil, want an error")
		}
	})
	t.Run("provenance is optional", func(t *testing.T) {
		a := valid()
		a.Provenance = provenance.DocumentRef{}
		if err := a.Validate(); err != nil {
			t.Errorf("Validate() = %v, want nil", err)
		}
	})
	t.Run("an undigested sbom is refused", func(t *testing.T) {
		a := valid()
		a.SBOMs = []provenance.DocumentRef{{Kind: provenance.DocumentSBOM, Format: "spdx-json"}}
		if err := a.Validate(); !errors.Is(err, provenance.ErrUnverifiableRef) {
			t.Errorf("got %v, want ErrUnverifiableRef", err)
		}
	})
}

func TestNewAssignsAnIDAndTimestamp(t *testing.T) {
	g := identity.NewFixedGenerator("11111111-1111-7111-8111-111111111111")
	c := identity.NewFixedClock(at)
	a, err := New(g, c, Artifact{
		ProjectID: "prj_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d002",
		Kind:      KindBinary,
		Digest:    dig,
		Locator:   "https://downloads.example.com/rollops",
		Size:      100,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if want := identity.ArtifactID("art_11111111-1111-7111-8111-111111111111"); a.ID != want {
		t.Errorf("ID = %q, want %q", a.ID, want)
	}
	if !a.CreatedAt.Equal(at) {
		t.Errorf("CreatedAt = %v, want %v", a.CreatedAt, at)
	}
}

// New validates, so an artifact cannot enter the system in a state Validate
// would later reject.
func TestNewRefusesAnInvalidArtifact(t *testing.T) {
	g := identity.NewGenerator()
	c := identity.NewSystemClock()
	if _, err := New(g, c, Artifact{Kind: KindBinary, Digest: dig, Locator: "x"}); err == nil {
		t.Error("New accepted an artifact with no project")
	}
}

func TestNewPropagatesAGeneratorFailure(t *testing.T) {
	boom := errors.New("no entropy")
	g := identity.GeneratorFunc(func() (string, error) { return "", boom })
	if _, err := New(g, identity.NewFixedClock(at), valid()); !errors.Is(err, boom) {
		t.Errorf("got %v, want the generator's error", err)
	}
}

// Identity is the digest, not the id: the same bytes pushed twice are one
// artifact, and a store that deduplicates on this cannot be fooled by a
// second upload under a new name.
func TestSameContentIsTheSameArtifact(t *testing.T) {
	a, b := valid(), valid()
	b.ID = "art_0199f3c1-8a2e-7b3d-9f10-2c5ab4e7d099"
	b.CreatedAt = at.Add(time.Hour)
	if !a.SameContent(b) {
		t.Error("identical content compared as different artifacts")
	}
	c := valid()
	c.Digest = digest.Of([]byte("other bytes"))
	if a.SameContent(c) {
		t.Error("different content compared as the same artifact")
	}
	d := valid()
	d.Kind = KindBinary
	if a.SameContent(d) {
		t.Error("the same bytes in a different kind compared as the same artifact")
	}
}
