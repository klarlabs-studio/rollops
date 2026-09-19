// Package targetv2 is the deployment-oriented Target contract (spec §9). It
// supersedes go.klarlabs.de/rollops/pkg/target, which stays until a major
// version boundary; see ADR-0006 and pkg/target/v1adapter.
//
// The one rule that is easy to get wrong: what a target can do is what
// Capabilities says, never what a type assertion says. Every Target value
// satisfies every optional subinterface, because a subprocess cannot be
// asked which Go interfaces it implements — a method for a capability the
// target lacks returns ErrUnsupported rather than being absent.
package targetv2

import "fmt"

// Capability is the name a capability goes by in a plugin manifest, where an
// operator authorizes it at install time.
type Capability string

const (
	CapabilityProgressiveDelivery Capability = "progressive-delivery"
	CapabilityNativeRollback      Capability = "rollback"
	CapabilityDriftDetection      Capability = "drift"
	CapabilityPrune               Capability = "prune"
	CapabilityTrafficSplitting    Capability = "traffic-splitting"
	CapabilityHealthObservation   Capability = "health-observation"
)

// Capabilities is what a target can do (spec §9.3). It is a struct of
// booleans rather than a set of strings so that adding a capability is a
// compile error at every target, and it is answered at runtime rather than
// derived from the target's name or type (INV-014).
type Capabilities struct {
	ProgressiveDelivery bool
	NativeRollback      bool
	DriftDetection      bool
	Prune               bool
	TrafficSplitting    bool
	HealthObservation   bool
}

// AllCapabilities lists every capability in the order overclaims and names are
// reported in. It returns a copy: the canonical order is a property of the
// contract, not something a caller may edit.
func AllCapabilities() []Capability {
	return []Capability{
		CapabilityProgressiveDelivery,
		CapabilityNativeRollback,
		CapabilityDriftDetection,
		CapabilityPrune,
		CapabilityTrafficSplitting,
		CapabilityHealthObservation,
	}
}

// field is the single place a name is tied to a field. Every other method
// goes through it, so a new capability that is added to the struct and to
// AllCapabilities but forgotten here fails TestEveryCapabilityHasAName rather
// than reporting itself as permanently unsupported.
func (c *Capabilities) field(name Capability) *bool {
	switch name {
	case CapabilityProgressiveDelivery:
		return &c.ProgressiveDelivery
	case CapabilityNativeRollback:
		return &c.NativeRollback
	case CapabilityDriftDetection:
		return &c.DriftDetection
	case CapabilityPrune:
		return &c.Prune
	case CapabilityTrafficSplitting:
		return &c.TrafficSplitting
	case CapabilityHealthObservation:
		return &c.HealthObservation
	default:
		return nil
	}
}

// Has reports whether the named capability is present. An unknown name is
// absent rather than an error: a host asked about something it does not know
// is being told no.
func (c Capabilities) Has(name Capability) bool {
	f := c.field(name)
	return f != nil && *f
}

// Set turns one named capability on or off. An unknown name is an error,
// because a caller writing a name is asserting it exists and silently
// dropping it would produce a target quietly missing what it declared.
func (c *Capabilities) Set(name Capability, on bool) error {
	f := c.field(name)
	if f == nil {
		return fmt.Errorf("targetv2: unknown capability %q", name)
	}
	*f = on
	return nil
}

// Names lists the capabilities that are present, in canonical order. This is
// the manifest form.
func (c Capabilities) Names() []Capability {
	var out []Capability
	for _, name := range AllCapabilities() {
		if c.Has(name) {
			out = append(out, name)
		}
	}
	return out
}

// ParseCapabilities reads the manifest form. Names this host does not know are
// returned rather than rejected: a plugin built against a newer contract is
// usable for everything both sides understand, and an unknown name in a
// ceiling caps nothing (ADR-0006).
func ParseCapabilities(names []string) (caps Capabilities, unknown []string) {
	for _, n := range names {
		if err := caps.Set(Capability(n), true); err != nil {
			unknown = append(unknown, n)
		}
	}
	return caps, unknown
}

// Narrow resolves the two answers §9 asks for into the one the host acts on.
// The receiver is what the manifest authorized at install time and the
// argument is what the bound target claims now; the result is the
// intersection, plus every capability the target claimed without being
// authorized for it.
//
// Claiming less than authorized is ordinary and is not reported: a plugin that
// supports drift in general, pointed at an environment where it cannot, is
// being honest about that environment. Claiming more is the other direction
// and §33.4 is why it is surfaced — an executable asking for more than it was
// installed with is a trust signal, not a nuisance.
func (c Capabilities) Narrow(claimed Capabilities) (effective Capabilities, overclaimed []Capability) {
	for _, name := range AllCapabilities() {
		switch {
		case claimed.Has(name) && c.Has(name):
			_ = effective.Set(name, true)
		case claimed.Has(name):
			overclaimed = append(overclaimed, name)
		}
	}
	return effective, overclaimed
}
