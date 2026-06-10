package studio

import "testing"

func TestBoundaryValidate(t *testing.T) {
	if err := (Boundary{}).Validate(); err != nil {
		t.Fatalf("default OSS boundary: %v", err)
	}
	if err := (Boundary{Workspace: OSSWorkspace}).Validate(); err != nil {
		t.Fatalf("oss workspace: %v", err)
	}
	if err := (Boundary{Workspace: "customer-a"}).Validate(); err == nil {
		t.Fatal("non-OSS workspace should require studio layer")
	}
}

func TestFleetDashboardContract(t *testing.T) {
	d := NewFleetDashboard([]FleetApp{
		{Workspace: "oss", Target: "a/prod/api", Phase: "promoted", Health: "Healthy", Sync: "Synced", Risk: "Low"},
		{Workspace: "customer-a", Target: "b/prod/api", Phase: "verifying", Health: "Progressing", Sync: "OutOfSync", Risk: "Medium"},
	})
	if d.Workspaces != 2 || len(d.Apps) != 2 {
		t.Fatalf("dashboard = %+v", d)
	}
}
