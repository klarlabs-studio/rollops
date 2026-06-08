// Package plugin defines the gRPC plugin protocol for third-party targets — the
// escape hatch that lets the community ship exotic targets without forking the
// core. A plugin is a subprocess (HashiCorp go-plugin style) that speaks the
// versioned RPC below; this package adapts that wire into the pkg/target.Target
// contract so a plugin-backed kind is indistinguishable from a first-party one
// and must pass the same conformance suite.
//
// The RPC interface is the seam: in production it is backed by go-plugin's gRPC
// transport; in tests it is an in-memory fake. Protocol versioning is enforced
// at handshake so a host and a plugin built against different protocol versions
// refuse to talk rather than misbehave.
package plugin

import (
	"context"
	"fmt"

	pt "go.klarlabs.de/rolloffs/pkg/target"
)

// ProtocolVersion is bumped on any breaking change to the RPC below.
const ProtocolVersion = 1

// Cookie is the magic handshake value; a mismatch means the subprocess is not a
// Rolloffs target plugin.
const Cookie = "ROLLOFFS_TARGET_PLUGIN_V1"

// Handshake is exchanged when a plugin starts.
type Handshake struct {
	ProtocolVersion int
	Cookie          string
}

// VerifyHandshake rejects a plugin built against a different protocol version or
// without the correct cookie.
func VerifyHandshake(h Handshake) error {
	if h.Cookie != Cookie {
		return fmt.Errorf("plugin: bad handshake cookie (not a target plugin)")
	}
	if h.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("plugin: protocol version mismatch: host %d, plugin %d", ProtocolVersion, h.ProtocolVersion)
	}
	return nil
}

// RPC is the wire a target plugin implements. Marshaling/transport (gRPC) lives
// in the production adapter; this interface keeps the semantics testable.
type RPC interface {
	Apply(ctx context.Context, kind string, spec []byte, checksum string) (changed bool, detail string, err error)
	Observe(ctx context.Context) (value string, meta map[string]string, err error)
	Health(ctx context.Context) (state int, reason string, err error)
}

// Target adapts an RPC into a pkg/target.Target.
type Target struct {
	rpc RPC
}

// NewTarget wraps an established plugin RPC connection.
func NewTarget(rpc RPC) *Target { return &Target{rpc: rpc} }

// Apply forwards to the plugin.
func (t *Target) Apply(ctx context.Context, m pt.Manifest) (pt.Result, error) {
	changed, detail, err := t.rpc.Apply(ctx, m.Kind, m.Spec, m.Checksum)
	if err != nil {
		return pt.Result{}, fmt.Errorf("plugin: apply: %w", err)
	}
	return pt.Result{Changed: changed, Detail: detail}, nil
}

// Observe forwards to the plugin.
func (t *Target) Observe(ctx context.Context) (pt.Fingerprint, error) {
	value, meta, err := t.rpc.Observe(ctx)
	if err != nil {
		return pt.Fingerprint{}, fmt.Errorf("plugin: observe: %w", err)
	}
	return pt.Fingerprint{Value: value, Meta: meta}, nil
}

// Health forwards to the plugin, mapping the wire state to HealthState.
func (t *Target) Health(ctx context.Context) (pt.HealthStatus, error) {
	state, reason, err := t.rpc.Health(ctx)
	if err != nil {
		return pt.HealthStatus{}, fmt.Errorf("plugin: health: %w", err)
	}
	return pt.HealthStatus{State: pt.HealthState(state), Reason: reason}, nil
}
