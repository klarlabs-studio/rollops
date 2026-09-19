package sqlite

import (
	"encoding/json"
	"fmt"
	"time"

	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/provenance"
	"go.klarlabs.de/rollops/internal/domain/value"
)

// Stored shapes are declared here rather than by tagging the domain types.
// A column is persisted data: renaming a domain field must not silently change
// what is on disk, and the domain should not carry storage concerns to make
// that safe. Every wire struct below is therefore explicit and tagged.

type documentRefRow struct {
	Kind    string `json:"kind"`
	Format  string `json:"format"`
	Locator string `json:"locator"`
	Digest  string `json:"digest"`
}

type sourceRevisionRow struct {
	Provider   string `json:"provider"`
	Repository string `json:"repository"`
	Revision   string `json:"revision"`
	Ref        string `json:"ref,omitempty"`
	TreeDigest string `json:"tree_digest,omitempty"`
	URL        string `json:"url,omitempty"`
}

type principalRow struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	DisplayName string            `json:"display_name,omitempty"`
	Claims      map[string]string `json:"claims,omitempty"`
}

type policyBindingRow struct {
	Name string `json:"name"`
	Ref  string `json:"ref"`
	Mode string `json:"mode"`
}

// encodeJSON renders v, or the given empty literal when there is nothing to
// store. A column is NOT NULL, so "nothing" still has a spelling.
func encodeJSON(v any, empty string) (string, error) {
	if v == nil {
		return empty, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("sqlite: encode: %w", err)
	}
	return string(b), nil
}

func decodeJSON(s string, v any) error {
	if err := json.Unmarshal([]byte(s), v); err != nil {
		return fmt.Errorf("sqlite: decode %q: %w", s, err)
	}
	return nil
}

func encodeStringMap(m map[string]string) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	return encodeJSON(m, "{}")
}

func decodeStringMap(s string) (map[string]string, error) {
	var m map[string]string
	if err := decodeJSON(s, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func encodeRefMap(m map[string]value.Ref) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	return encodeJSON(m, "{}")
}

func decodeRefMap(s string) (map[string]value.Ref, error) {
	var m map[string]value.Ref
	if err := decodeJSON(s, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// encodeTime stores an instant, not a rendering: it is normalised to UTC so
// that two writes of the same moment produce the same text and sort together.
func encodeTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func decodeTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("sqlite: decode time %q: %w", s, err)
	}
	return t, nil
}

func encodeDocumentRef(d provenance.DocumentRef) (string, error) {
	if d == (provenance.DocumentRef{}) {
		return "null", nil
	}
	return encodeJSON(documentRefRow{
		Kind:    string(d.Kind),
		Format:  d.Format,
		Locator: d.Locator,
		Digest:  d.Digest.String(),
	}, "null")
}

func decodeDocumentRef(s string) (provenance.DocumentRef, error) {
	var row *documentRefRow
	if err := decodeJSON(s, &row); err != nil {
		return provenance.DocumentRef{}, err
	}
	if row == nil {
		return provenance.DocumentRef{}, nil
	}
	return documentRefFrom(*row)
}

func documentRefFrom(row documentRefRow) (provenance.DocumentRef, error) {
	d, err := digest.Parse(row.Digest)
	if err != nil {
		return provenance.DocumentRef{}, fmt.Errorf("sqlite: decode document ref: %w", err)
	}
	return provenance.DocumentRef{
		Kind:    provenance.DocumentKind(row.Kind),
		Format:  row.Format,
		Locator: row.Locator,
		Digest:  d,
	}, nil
}

func encodeDocumentRefs(ds []provenance.DocumentRef) (string, error) {
	if len(ds) == 0 {
		return "[]", nil
	}
	rows := make([]documentRefRow, len(ds))
	for i, d := range ds {
		rows[i] = documentRefRow{
			Kind:    string(d.Kind),
			Format:  d.Format,
			Locator: d.Locator,
			Digest:  d.Digest.String(),
		}
	}
	return encodeJSON(rows, "[]")
}

func decodeDocumentRefs(s string) ([]provenance.DocumentRef, error) {
	var rows []documentRefRow
	if err := decodeJSON(s, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ds := make([]provenance.DocumentRef, len(rows))
	for i, row := range rows {
		d, err := documentRefFrom(row)
		if err != nil {
			return nil, err
		}
		ds[i] = d
	}
	return ds, nil
}

func encodeSourceRevision(r provenance.SourceRevision) (string, error) {
	return encodeJSON(sourceRevisionRow{
		Provider:   r.Provider,
		Repository: r.Repository,
		Revision:   r.Revision,
		Ref:        r.Ref,
		TreeDigest: r.TreeDigest,
		URL:        r.URL,
	}, "{}")
}

func decodeSourceRevision(s string) (provenance.SourceRevision, error) {
	var row sourceRevisionRow
	if err := decodeJSON(s, &row); err != nil {
		return provenance.SourceRevision{}, err
	}
	return provenance.SourceRevision{
		Provider:   row.Provider,
		Repository: row.Repository,
		Revision:   row.Revision,
		Ref:        row.Ref,
		TreeDigest: row.TreeDigest,
		URL:        row.URL,
	}, nil
}

// encodePrincipal redacts before storing. Attribution is written on every
// surface that renders a release, so a credential that reached the column would
// be impossible to recall (INV-011).
func encodePrincipal(p identity.Principal) (string, error) {
	p = p.Redacted()
	return encodeJSON(principalRow{
		ID:          p.ID,
		Type:        string(p.Type),
		DisplayName: p.DisplayName,
		Claims:      p.Claims,
	}, "{}")
}

func decodePrincipal(s string) (identity.Principal, error) {
	var row principalRow
	if err := decodeJSON(s, &row); err != nil {
		return identity.Principal{}, err
	}
	return identity.Principal{
		ID:          row.ID,
		Type:        identity.PrincipalType(row.Type),
		DisplayName: row.DisplayName,
		Claims:      row.Claims,
	}, nil
}

func encodePolicyBindings(ps []environment.PolicyBinding) (string, error) {
	if len(ps) == 0 {
		return "[]", nil
	}
	rows := make([]policyBindingRow, len(ps))
	for i, p := range ps {
		rows[i] = policyBindingRow{Name: p.Name, Ref: p.Ref, Mode: string(p.Mode)}
	}
	return encodeJSON(rows, "[]")
}

func decodePolicyBindings(s string) ([]environment.PolicyBinding, error) {
	var rows []policyBindingRow
	if err := decodeJSON(s, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ps := make([]environment.PolicyBinding, len(rows))
	for i, row := range rows {
		ps[i] = environment.PolicyBinding{
			Name: row.Name,
			Ref:  row.Ref,
			Mode: environment.PolicyMode(row.Mode),
		}
	}
	return ps, nil
}
