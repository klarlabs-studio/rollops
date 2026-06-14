package plugin

// APIVersion is the plugin protocol version the host and SDK negotiate.
const APIVersion = "v1"

// RiskClass coarsely ranks how invasive a plugin's tools are; the host's
// safety policy caps the risk class it will admit.
type RiskClass string

const (
	RiskPassive  RiskClass = "passive"  // reads only, no external mutation
	RiskActive   RiskClass = "active"   // mutates the declared target
	RiskInvasive RiskClass = "invasive" // broad or irreversible effects
)

// Tool is one invocable operation within a capability.
type Tool struct {
	Name        string
	Description string
	Mutating    bool
	// RiskClass optionally rates this tool's risk. When tools set it but the
	// plugin-wide Safety.RiskClass is unset, the host uses the highest tool risk
	// as the plugin's effective risk for policy admission.
	RiskClass RiskClass
}

// Capability is a named group of tools (e.g. "target", "featureflag").
type Capability struct {
	Name        string
	Description string
	Tools       []Tool
}

// Safety declares the scopes a plugin needs. The host rejects a plugin whose
// requirements exceed the active policy.
type Safety struct {
	NetworkHosts      []string
	FilePaths         []string
	EnvVars           []string
	RiskClass         RiskClass
	NeedsConfirmation bool
}

// Manifest is what a plugin advertises at handshake.
type Manifest struct {
	Name         string
	Version      string
	Capabilities []Capability
	Safety       Safety
}

// ManifestBuilder assembles a Manifest fluently:
//
//	m := NewManifest("acme/exotic", "1.0.0").
//		Capability("target", "Exotic deploy target").
//			Tool("apply", "Deploy desired state", true).
//			Tool("observe", "Report live fingerprint", false).
//			Tool("health", "Report health", false).
//		Done().
//		Safety(Safety{NetworkHosts: []string{"api.acme.com:443"}, RiskClass: RiskActive}).
//		Build()
type ManifestBuilder struct {
	m   Manifest
	cap *Capability
}

// NewManifest starts a builder for a plugin with the given name and version.
func NewManifest(name, version string) *ManifestBuilder {
	return &ManifestBuilder{m: Manifest{Name: name, Version: version}}
}

// Capability opens a new capability; subsequent Tool calls attach to it until
// Done (or the next Capability / Build).
func (b *ManifestBuilder) Capability(name, description string) *ManifestBuilder {
	b.flush()
	b.cap = &Capability{Name: name, Description: description}
	return b
}

// Tool adds a tool to the open capability.
func (b *ManifestBuilder) Tool(name, description string, mutating bool) *ManifestBuilder {
	if b.cap != nil {
		b.cap.Tools = append(b.cap.Tools, Tool{Name: name, Description: description, Mutating: mutating})
	}
	return b
}

// ToolRisk adds a tool with an explicit per-tool risk class.
func (b *ManifestBuilder) ToolRisk(name, description string, mutating bool, risk RiskClass) *ManifestBuilder {
	if b.cap != nil {
		b.cap.Tools = append(b.cap.Tools, Tool{Name: name, Description: description, Mutating: mutating, RiskClass: risk})
	}
	return b
}

// Done closes the open capability.
func (b *ManifestBuilder) Done() *ManifestBuilder {
	b.flush()
	return b
}

// Safety sets the safety requirements.
func (b *ManifestBuilder) Safety(s Safety) *ManifestBuilder {
	b.m.Safety = s
	return b
}

// Build returns the assembled manifest.
func (b *ManifestBuilder) Build() Manifest {
	b.flush()
	return b.m
}

func (b *ManifestBuilder) flush() {
	if b.cap != nil {
		b.m.Capabilities = append(b.m.Capabilities, *b.cap)
		b.cap = nil
	}
}
