package governance

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// HTTPProvider asks an external governor whether a rollout may proceed.
//
// Deliberately generic. This is not an integration with a named product: it posts a
// documented request and reads a documented decision, so anything answering that
// contract works — a change-governance tool, a policy service, an internal approval
// system, a script. Naming a product here would couple this repository's release
// cadence to that product's, and oblige every Rollops user to acquire it.
//
// Configured entirely from the environment, matching how notify is wired, so a
// signing secret never has to live in a config file that gets committed.
type HTTPProvider struct {
	URL     string
	Secret  string
	Timeout time.Duration

	// Client is injectable for tests.
	Client *http.Client
}

// defaultGovernanceTimeout bounds the wait.
//
// Short on purpose: this sits in the deploy path, and a governor that has not
// answered in a few seconds is unavailable for practical purposes. Waiting longer
// converts an unreachable governor into a stalled rollout, which is a worse failure
// than a refused one — a refusal is legible and can be overridden.
const defaultGovernanceTimeout = 5 * time.Second

// FromEnv builds a provider from ROLLOPS_GOVERNANCE_URL and optional
// ROLLOPS_GOVERNANCE_SECRET / ROLLOPS_GOVERNANCE_TIMEOUT.
//
// Returns nil when no URL is set, which is the ordinary case: a user who has not
// asked for external governance must be entirely unaffected by this existing.
// getenv is injectable for tests, matching notify.FromEnv.
func FromEnv(getenv func(string) string) Provider {
	url := getenv("ROLLOPS_GOVERNANCE_URL")
	if url == "" {
		return nil
	}

	timeout := defaultGovernanceTimeout
	if raw := getenv("ROLLOPS_GOVERNANCE_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			timeout = parsed
		}
		// An unparseable timeout keeps the default rather than failing construction.
		// Refusing to start because a duration was mistyped would take the deploy path
		// down over a formatting error; the default is a safe, documented value.
	}

	return &HTTPProvider{
		URL:     url,
		Secret:  getenv("ROLLOPS_GOVERNANCE_SECRET"),
		Timeout: timeout,
	}
}

// wireRequest is what a governor receives. Field names are snake_case and stable:
// this is a contract with software outside this repository, so it changes only
// deliberately.
type wireRequest struct {
	Action      string `json:"action"`
	TargetRef   string `json:"target_ref"`
	Environment string `json:"environment,omitempty"`
	Version     string `json:"version,omitempty"`
	ActorID     string `json:"actor_id,omitempty"`
	ActorKind   string `json:"actor_kind,omitempty"`
}

// wireDecision is what a governor returns.
type wireDecision struct {
	Allowed  bool              `json:"allowed"`
	Reason   string            `json:"reason,omitempty"`
	Evidence map[string]string `json:"evidence,omitempty"`
}

// Evaluate asks the governor and returns its decision.
//
// Every failure path denies. A configured governor that cannot be reached is not
// the same as no governor configured: the first is a failure, the second is a
// choice, and giving them the same outcome would make the gate disappear exactly
// when a rushed deploy is most likely — during an incident, on a bad network,
// mid-migration. The error explains which, so an operator can tell a refusal from
// an outage.
func (p *HTTPProvider) Evaluate(ctx context.Context, req Request) (Decision, error) {
	body, err := json.Marshal(wireRequest{
		Action:      req.Action,
		TargetRef:   req.TargetRef,
		Environment: req.Environment,
		Version:     req.Version,
		ActorID:     req.Actor.Name,
		ActorKind:   req.Actor.Kind,
	})
	if err != nil {
		return Decision{}, fmt.Errorf("governance: encode request: %w", err)
	}

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultGovernanceTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.URL, bytes.NewReader(body))
	if err != nil {
		return Decision{}, fmt.Errorf("governance: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.Secret != "" {
		mac := hmac.New(sha256.New, []byte(p.Secret))
		mac.Write(body)
		httpReq.Header.Set("X-Rollops-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return Decision{}, fmt.Errorf("governance: %s is unreachable: %w", p.URL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		// A governor answering 500 has not decided anything. Treating a broken
		// governor as permission would be the same failure as treating an unreachable
		// one as permission.
		return Decision{}, fmt.Errorf("governance: %s answered %s", p.URL, resp.Status)
	}

	var decoded wireDecision
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return Decision{}, fmt.Errorf("governance: unreadable decision from %s: %w", p.URL, err)
	}

	// Mapped field by field rather than converted, even though the two structs happen
	// to line up today. wireDecision is a contract with software outside this
	// repository; Decision is ours to change. A conversion would tie them together, so
	// adding a field to Decision would break this line and invite someone to "fix" it
	// by editing the wire type — silently changing what every governor must send.
	//nolint:staticcheck // S1016: deliberate, see above.
	return Decision{
		Allowed:  decoded.Allowed,
		Reason:   decoded.Reason,
		Evidence: decoded.Evidence,
	}, nil
}
