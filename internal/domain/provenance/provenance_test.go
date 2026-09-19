package provenance

import (
	"errors"
	"strings"
	"testing"

	"go.klarlabs.de/rollops/internal/domain/digest"
)

const commit = "9f2c1b7e4a6d8c0f3e5b2a9d7c4f1e8b6a3d0c27"

func gitRevision() SourceRevision {
	return SourceRevision{
		Provider:   ProviderGit,
		Repository: "github.com/klarlabs-studio/rollops",
		Revision:   commit,
		Ref:        "refs/heads/main",
		URL:        "https://github.com/klarlabs-studio/rollops",
	}
}

func TestSourceRevisionAcceptsAResolvedCommit(t *testing.T) {
	if err := gitRevision().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// INV-002: a release is reproducible only if its source revision is immutable.
// A branch or tag name resolves to different content over time, so accepting
// one here would make the release a description rather than a fact.
func TestSourceRevisionRejectsAMutableRevision(t *testing.T) {
	cases := []struct {
		name string
		rev  string
	}{
		{"branch", "main"},
		{"head", "HEAD"},
		{"tag", "v1.4.2"},
		{"short sha", commit[:7]},
		{"uppercase sha", strings.ToUpper(commit)},
		{"not hex", strings.Repeat("z", 40)},
		{"empty", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := gitRevision()
			r.Revision = c.rev
			if err := r.Validate(); err == nil {
				t.Errorf("Validate() accepted revision %q", c.rev)
			}
		})
	}
}

func TestSourceRevisionAcceptsASHA256Commit(t *testing.T) {
	r := gitRevision()
	r.Revision = strings.Repeat("ab", 32)
	if err := r.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil for a sha256 object id", err)
	}
}

// Providers other than git number their revisions differently, so the hex rule
// must not be applied where it does not hold.
func TestSourceRevisionAcceptsANonGitProvider(t *testing.T) {
	r := SourceRevision{
		Provider:   "svn",
		Repository: "svn.example.com/trunk",
		Revision:   "48211",
	}
	if err := r.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestSourceRevisionRequiresProviderAndRepository(t *testing.T) {
	t.Run("provider", func(t *testing.T) {
		r := gitRevision()
		r.Provider = ""
		if err := r.Validate(); err == nil {
			t.Error("Validate() accepted a revision with no provider")
		}
	})
	t.Run("repository", func(t *testing.T) {
		r := gitRevision()
		r.Repository = " "
		if err := r.Validate(); err == nil {
			t.Error("Validate() accepted a revision with no repository")
		}
	})
}

func TestZeroSourceRevisionIsInvalid(t *testing.T) {
	var r SourceRevision
	if err := r.Validate(); err == nil {
		t.Error("the zero SourceRevision validated")
	}
}

func TestDocumentRefValidates(t *testing.T) {
	d := digest.Of([]byte("an sbom"))
	cases := []struct {
		name string
		ref  DocumentRef
		ok   bool
	}{
		{"complete", DocumentRef{Kind: DocumentSBOM, Format: "spdx-json", Locator: "oci://x", Digest: d}, true},
		{"no locator is fine when inline elsewhere", DocumentRef{Kind: DocumentSBOM, Format: "spdx-json", Digest: d}, true},
		{"missing kind", DocumentRef{Format: "spdx-json", Digest: d}, false},
		{"unknown kind", DocumentRef{Kind: DocumentKind("rumour"), Format: "spdx-json", Digest: d}, false},
		{"missing format", DocumentRef{Kind: DocumentSBOM, Digest: d}, false},
		{"missing digest", DocumentRef{Kind: DocumentSBOM, Format: "spdx-json"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.ref.Validate()
			if c.ok && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
			if !c.ok && err == nil {
				t.Error("Validate() = nil, want an error")
			}
		})
	}
}

// A reference whose digest is absent points at whatever the locator serves
// today, which is exactly the property an attestation is supposed to remove.
func TestDocumentRefWithoutADigestIsRefused(t *testing.T) {
	ref := DocumentRef{Kind: DocumentSignature, Format: "cosign-bundle", Locator: "https://rekor.example/1"}
	if err := ref.Validate(); !errors.Is(err, ErrUnverifiableRef) {
		t.Errorf("got %v, want ErrUnverifiableRef", err)
	}
}

func TestDocumentRefKindMustMatchItsSlot(t *testing.T) {
	sbom := DocumentRef{Kind: DocumentSBOM, Format: "spdx-json", Digest: digest.Of([]byte("s"))}
	if err := sbom.ValidateAs(DocumentSignature); err == nil {
		t.Error("an sbom was accepted where a signature belongs")
	}
	if err := sbom.ValidateAs(DocumentSBOM); err != nil {
		t.Errorf("ValidateAs(DocumentSBOM) = %v, want nil", err)
	}
}

func TestProvenanceValidates(t *testing.T) {
	p := Provenance{
		Source:       gitRevision(),
		Builder:      "https://github.com/klarlabs-studio/rollops/.github/workflows/ci.yml",
		BuildType:    "https://slsa.dev/container-based-build/v0.1",
		InvocationID: "run-1842",
		Materials: []Material{
			{URI: "pkg:golang/go.klarlabs.de/rollops@v0.9.0", Digest: digest.Of([]byte("m"))},
			// A material may be named without a digest; unlike an attestation,
			// it is a record of an input rather than a claim about one.
			{URI: "git+https://github.com/klarlabs-studio/rollops@" + commit},
		},
		Attestations: []DocumentRef{
			{Kind: DocumentAttestation, Format: "slsa-v1", Digest: digest.Of([]byte("a"))},
		},
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestProvenanceRejectsABadMember(t *testing.T) {
	t.Run("source", func(t *testing.T) {
		p := Provenance{Builder: "b", BuildType: "t"}
		if err := p.Validate(); err == nil {
			t.Error("Validate() accepted provenance with no source revision")
		}
	})
	t.Run("builder", func(t *testing.T) {
		p := Provenance{Source: gitRevision(), BuildType: "t"}
		if err := p.Validate(); err == nil {
			t.Error("Validate() accepted provenance with no builder")
		}
	})
	t.Run("build type", func(t *testing.T) {
		p := Provenance{Source: gitRevision(), Builder: "b"}
		if err := p.Validate(); err == nil {
			t.Error("Validate() accepted provenance with no build type")
		}
	})
	t.Run("material", func(t *testing.T) {
		p := Provenance{
			Source:    gitRevision(),
			Builder:   "b",
			BuildType: "t",
			Materials: []Material{{URI: "", Digest: digest.Of([]byte("m"))}},
		}
		if err := p.Validate(); err == nil {
			t.Error("Validate() accepted a material with no uri")
		}
	})
	t.Run("attestation of the wrong kind", func(t *testing.T) {
		p := Provenance{
			Source:       gitRevision(),
			Builder:      "b",
			BuildType:    "t",
			Attestations: []DocumentRef{{Kind: DocumentSBOM, Format: "spdx-json", Digest: digest.Of([]byte("a"))}},
		}
		if err := p.Validate(); err == nil {
			t.Error("Validate() accepted an sbom in the attestation list")
		}
	})
}
