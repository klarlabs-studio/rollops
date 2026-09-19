package identity

import (
	"errors"
	"fmt"
	"strings"
)

// PrincipalType distinguishes who or what performed an action. Agents are named
// separately from humans because the guardrails that apply to them differ, and
// an audit trail that cannot tell them apart cannot answer the question anyone
// actually asks after an incident.
type PrincipalType string

const (
	PrincipalHuman   PrincipalType = "human"
	PrincipalAgent   PrincipalType = "agent"
	PrincipalService PrincipalType = "service"
	PrincipalSystem  PrincipalType = "system"
)

// Principal is the actor a mutation is attributed to (INV-005). Every command
// that changes state carries one; there is no anonymous path.
type Principal struct {
	ID     string
	Type   PrincipalType
	Claims map[string]string
}

// redacted replaces any claim value that may carry credential material.
const redacted = "[redacted]"

// Claim keys whose values are treated as secret. Matching is on a lowercased
// substring, so "x-authorization" and "API_KEY" are both caught.
//
// This is a denylist and therefore incomplete: an identity provider that names
// its bearer token something unexpected will pass straight through. Treat it as
// the last line of defence, not the only one — claims that are known to be
// sensitive should not be copied onto a Principal in the first place.
var secretClaimFragments = []string{
	"apikey",
	"api_key",
	"authorization",
	"credential",
	"passwd",
	"password",
	"private_key",
	"secret",
	"token",
}

// ErrInvalidPrincipal reports an actor that cannot be attributed.
var ErrInvalidPrincipal = errors.New("identity: invalid principal")

// Validate reports whether the principal can be attributed. The zero value
// never can.
func (p Principal) Validate() error {
	if strings.TrimSpace(p.ID) == "" {
		return fmt.Errorf("%w: missing id", ErrInvalidPrincipal)
	}
	switch p.Type {
	case PrincipalHuman, PrincipalAgent, PrincipalService, PrincipalSystem:
	default:
		return fmt.Errorf("%w: unknown type %q", ErrInvalidPrincipal, p.Type)
	}
	return nil
}

// Redacted returns a copy safe to render in an audit entry, an API response or
// a log (INV-012). The receiver is untouched.
func (p Principal) Redacted() Principal {
	if p.Claims == nil {
		return p
	}
	claims := make(map[string]string, len(p.Claims))
	for k, v := range p.Claims {
		if isSecretClaim(k) {
			claims[k] = redacted
			continue
		}
		claims[k] = v
	}
	p.Claims = claims
	return p
}

func isSecretClaim(key string) bool {
	k := strings.ToLower(key)
	for _, fragment := range secretClaimFragments {
		if strings.Contains(k, fragment) {
			return true
		}
	}
	return false
}
