package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"go.klarlabs.de/rollops/internal/domain/digest"
	"go.klarlabs.de/rollops/internal/domain/environment"
	"go.klarlabs.de/rollops/internal/domain/identity"
	"go.klarlabs.de/rollops/internal/domain/plan"
	"go.klarlabs.de/rollops/internal/domain/policy"
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

type changeRow struct {
	Path      string `json:"path"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Sensitive bool   `json:"sensitive,omitempty"`
}

type operationRow struct {
	ID           string      `json:"id"`
	Target       string      `json:"target"`
	Kind         string      `json:"kind"`
	Summary      string      `json:"summary,omitempty"`
	Changes      []changeRow `json:"changes,omitempty"`
	Dependencies []string    `json:"dependencies,omitempty"`
	Reversible   bool        `json:"reversible,omitempty"`
}

type rollbackRow struct {
	FromRelease string         `json:"from_release"`
	ToRelease   string         `json:"to_release"`
	Operations  []operationRow `json:"operations,omitempty"`
	Automatic   bool           `json:"automatic,omitempty"`
}

type riskFactorRow struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type riskRow struct {
	Level   string          `json:"level"`
	Score   float64         `json:"score"`
	Factors []riskFactorRow `json:"factors,omitempty"`
}

type requirementRow struct {
	Type   string `json:"type"`
	Role   string `json:"role,omitempty"`
	Count  int    `json:"count,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type reasonRow struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type decisionRow struct {
	Allowed      bool             `json:"allowed"`
	Requirements []requirementRow `json:"requirements,omitempty"`
	Reasons      []reasonRow      `json:"reasons,omitempty"`
	Risk         riskRow          `json:"risk"`
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

// encodeTimePtr stores an absent instant as NULL rather than as a zero time,
// because "has not started" and "started at the epoch" are different facts and
// a column that spelled them alike would lose one of them.
func encodeTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return encodeTime(*t)
}

func decodeTimePtr(s sql.NullString) (*time.Time, error) {
	if !s.Valid {
		return nil, nil
	}
	t, err := decodeTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// operationRows redacts as it builds. The value of a change marked sensitive
// never reaches a column (INV-011), and every operation a plan stores passes
// through here, so the boundary enforces it rather than trusting each caller to
// have called Redacted first. The plan hash already excludes these values, so
// what is read back still verifies against the hash it was approved under.
func operationRows(ops []plan.PlannedOperation) []operationRow {
	rows := make([]operationRow, len(ops))
	for i, o := range ops {
		row := operationRow{
			ID:         string(o.ID),
			Target:     o.Target,
			Kind:       string(o.Kind),
			Summary:    o.Summary,
			Reversible: o.Reversible,
		}
		for _, d := range o.Dependencies {
			row.Dependencies = append(row.Dependencies, string(d))
		}
		for _, c := range o.Diff.Changes {
			cr := changeRow{Path: c.Path, Sensitive: c.Sensitive}
			if !c.Sensitive {
				cr.From, cr.To = c.From, c.To
			}
			row.Changes = append(row.Changes, cr)
		}
		rows[i] = row
	}
	return rows
}

func operationsFrom(rows []operationRow) []plan.PlannedOperation {
	if len(rows) == 0 {
		return nil
	}
	ops := make([]plan.PlannedOperation, len(rows))
	for i, row := range rows {
		o := plan.PlannedOperation{
			ID:         plan.OperationID(row.ID),
			Target:     row.Target,
			Kind:       plan.OperationKind(row.Kind),
			Summary:    row.Summary,
			Reversible: row.Reversible,
		}
		for _, d := range row.Dependencies {
			o.Dependencies = append(o.Dependencies, plan.OperationID(d))
		}
		for _, c := range row.Changes {
			o.Diff.Changes = append(o.Diff.Changes, plan.Change{
				Path:      c.Path,
				From:      c.From,
				To:        c.To,
				Sensitive: c.Sensitive,
			})
		}
		ops[i] = o
	}
	return ops
}

func encodeOperations(ops []plan.PlannedOperation) (string, error) {
	if len(ops) == 0 {
		return "[]", nil
	}
	return encodeJSON(operationRows(ops), "[]")
}

func decodeOperations(s string) ([]plan.PlannedOperation, error) {
	var rows []operationRow
	if err := decodeJSON(s, &rows); err != nil {
		return nil, err
	}
	return operationsFrom(rows), nil
}

// encodeRollback stores an absent rollback as NULL. A plan that names no way
// back is a real answer — some changes have none — and spelling it as an empty
// object would make it indistinguishable from one whose fields were lost.
func encodeRollback(r plan.RollbackPlan) (string, error) {
	if r.FromRelease == "" && r.ToRelease == "" && len(r.Operations) == 0 && !r.Automatic {
		return "null", nil
	}
	return encodeJSON(rollbackRow{
		FromRelease: string(r.FromRelease),
		ToRelease:   string(r.ToRelease),
		Operations:  operationRows(r.Operations),
		Automatic:   r.Automatic,
	}, "null")
}

func decodeRollback(s string) (plan.RollbackPlan, error) {
	var row *rollbackRow
	if err := decodeJSON(s, &row); err != nil {
		return plan.RollbackPlan{}, err
	}
	if row == nil {
		return plan.RollbackPlan{}, nil
	}
	return plan.RollbackPlan{
		FromRelease: identity.ReleaseID(row.FromRelease),
		ToRelease:   identity.ReleaseID(row.ToRelease),
		Operations:  operationsFrom(row.Operations),
		Automatic:   row.Automatic,
	}, nil
}

func encodeDecision(d policy.Decision) (string, error) {
	row := decisionRow{
		Allowed: d.Allowed,
		Risk: riskRow{
			Level: string(d.Risk.Level),
			Score: d.Risk.Score,
		},
	}
	for _, f := range d.Risk.Factors {
		row.Risk.Factors = append(row.Risk.Factors, riskFactorRow{Code: f.Code, Message: f.Message})
	}
	for _, r := range d.Requirements {
		row.Requirements = append(row.Requirements, requirementRow{
			Type:   string(r.Type),
			Role:   r.Role,
			Count:  r.Count,
			Detail: r.Detail,
		})
	}
	for _, r := range d.Reasons {
		row.Reasons = append(row.Reasons, reasonRow{Code: r.Code, Message: r.Message})
	}
	return encodeJSON(row, "{}")
}

func decodeDecision(s string) (policy.Decision, error) {
	var row decisionRow
	if err := decodeJSON(s, &row); err != nil {
		return policy.Decision{}, err
	}
	d := policy.Decision{
		Allowed: row.Allowed,
		Risk: policy.RiskAssessment{
			Level: policy.RiskLevel(row.Risk.Level),
			Score: row.Risk.Score,
		},
	}
	for _, f := range row.Risk.Factors {
		d.Risk.Factors = append(d.Risk.Factors, policy.RiskFactor{Code: f.Code, Message: f.Message})
	}
	for _, r := range row.Requirements {
		d.Requirements = append(d.Requirements, policy.Requirement{
			Type:   policy.RequirementType(r.Type),
			Role:   r.Role,
			Count:  r.Count,
			Detail: r.Detail,
		})
	}
	for _, r := range row.Reasons {
		d.Reasons = append(d.Reasons, policy.Reason{Code: r.Code, Message: r.Message})
	}
	return d, nil
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
