package studio

import "fmt"

const OSSWorkspace = "oss"

// Boundary enforces the open-core line: OSS Rollops is single-workspace. Managed
// multi-customer orchestration belongs in the studio layer above core.
type Boundary struct {
	Workspace string
}

func (b Boundary) Validate() error {
	if b.Workspace == "" || b.Workspace == OSSWorkspace {
		return nil
	}
	return fmt.Errorf("studio: workspace %q requires managed studio layer", b.Workspace)
}

type FleetApp struct {
	Workspace string `json:"workspace"`
	Target    string `json:"target"`
	Phase     string `json:"phase"`
	Health    string `json:"health"`
	Sync      string `json:"sync"`
	Risk      string `json:"risk"`
}

type FleetDashboard struct {
	Workspaces int        `json:"workspaces"`
	Apps       []FleetApp `json:"apps"`
}

func NewFleetDashboard(apps []FleetApp) FleetDashboard {
	seen := map[string]bool{}
	for _, app := range apps {
		ws := app.Workspace
		if ws == "" {
			ws = OSSWorkspace
		}
		seen[ws] = true
	}
	return FleetDashboard{Workspaces: len(seen), Apps: apps}
}
