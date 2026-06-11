package plugin

// Capability and tool names, plus the JSON tool payloads shared by the SDK
// (plugin side) and the host adapters. Keeping them in one public file makes
// the wire contract a single source of truth for plugin authors.

// Capability names.
const (
	CapabilityTarget      = "target"
	CapabilityFeatureFlag = "featureflag"
)

// target capability tools.
const (
	ToolApply   = "apply"
	ToolObserve = "observe"
	ToolHealth  = "health"
)

// featureflag capability tool.
const ToolApplyFlag = "apply_flag"

// ApplyInput is the target.apply payload.
type ApplyInput struct {
	Kind     string `json:"kind"`
	Spec     []byte `json:"spec"`
	Checksum string `json:"checksum"`
}

// ApplyOutput is the target.apply result.
type ApplyOutput struct {
	Changed bool   `json:"changed"`
	Detail  string `json:"detail"`
}

// ObserveOutput is the target.observe result.
type ObserveOutput struct {
	Value string            `json:"value"`
	Meta  map[string]string `json:"meta"`
}

// HealthOutput is the target.health result.
type HealthOutput struct {
	State  int    `json:"state"` // pkg/target.HealthState
	Reason string `json:"reason"`
}

// FlagChange is the featureflag.apply_flag payload.
type FlagChange struct {
	Flag        string `json:"flag"`
	Environment string `json:"environment"`
	Percentage  int    `json:"percentage"`
	Disabled    bool   `json:"disabled"`
}
