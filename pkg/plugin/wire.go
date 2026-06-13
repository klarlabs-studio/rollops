package plugin

// Capability and tool names, plus the JSON tool payloads shared by the SDK
// (plugin side) and the host adapters. Keeping them in one public file makes
// the wire contract a single source of truth for plugin authors.

// Capability names.
const (
	CapabilityTarget        = "target"
	CapabilityFeatureFlag   = "featureflag"
	CapabilityTrafficRouter = "trafficrouter"
)

// target capability tools.
const (
	ToolApply   = "apply"
	ToolObserve = "observe"
	ToolHealth  = "health"
)

// featureflag capability tool.
const ToolApplyFlag = "apply_flag"

// trafficrouter capability tool.
const ToolSetWeight = "set_weight"

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

// TrafficChange is the trafficrouter.set_weight payload: shift Weight percent of
// traffic to the canary backend, the remainder to the stable backend, on the
// named route.
type TrafficChange struct {
	Route         string `json:"route"`         // router object name (e.g. Gateway API HTTPRoute)
	Namespace     string `json:"namespace"`     // router object namespace
	StableService string `json:"stableService"` // backend receiving (100-weight)%
	CanaryService string `json:"canaryService"` // backend receiving weight%
	Weight        int    `json:"weight"`        // canary traffic percentage 0..100
}
