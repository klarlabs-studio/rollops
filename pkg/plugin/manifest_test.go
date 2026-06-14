package plugin

import (
	"context"
	"encoding/json"
	"testing"
)

func TestManifestBuilder(t *testing.T) {
	m := NewManifest("acme/exotic", "1.2.0").
		Capability(CapabilityTarget, "deploy").
		Tool(ToolApply, "apply", true).
		Tool(ToolObserve, "observe", false).
		Done().
		Safety(Safety{NetworkHosts: []string{"api:443"}, RiskClass: RiskActive}).
		Build()
	if m.Name != "acme/exotic" || m.Version != "1.2.0" {
		t.Fatalf("meta = %+v", m)
	}
	if len(m.Capabilities) != 1 || m.Capabilities[0].Name != CapabilityTarget {
		t.Fatalf("caps = %+v", m.Capabilities)
	}
	if len(m.Capabilities[0].Tools) != 2 || !m.Capabilities[0].Tools[0].Mutating {
		t.Errorf("tools = %+v", m.Capabilities[0].Tools)
	}
	if m.Safety.RiskClass != RiskActive || m.Safety.NetworkHosts[0] != "api:443" {
		t.Errorf("safety = %+v", m.Safety)
	}
}

func TestManifestBuilder_ToolRisk(t *testing.T) {
	m := NewManifest("p", "1").
		Capability(CapabilityTarget, "deploy").
		ToolRisk(ToolApply, "apply", true, RiskInvasive).
		Tool(ToolObserve, "observe", false).
		Done().
		Build()
	tools := m.Capabilities[0].Tools
	if tools[0].RiskClass != RiskInvasive {
		t.Errorf("apply risk = %q, want invasive", tools[0].RiskClass)
	}
	if tools[1].RiskClass != "" {
		t.Errorf("observe risk = %q, want unset", tools[1].RiskClass)
	}
}

func TestServer_RoutesAndRejects(t *testing.T) {
	srv := NewServer(NewManifest("p", "1").Build()).
		HandleTool("cap", "echo", func(_ context.Context, in []byte) ([]byte, error) { return in, nil })

	out, err := srv.InvokeTool(context.Background(), invoke("cap", "echo", []byte(`"hi"`)))
	if err != nil || string(out.GetOutput()) != `"hi"` {
		t.Fatalf("echo = %q err=%v", out.GetOutput(), err)
	}
	if _, err := srv.InvokeTool(context.Background(), invoke("cap", "nope", nil)); err == nil {
		t.Error("unknown tool must error")
	}

	mresp, _ := srv.GetManifest(context.Background(), nil)
	if mresp.GetApiVersion() != APIVersion {
		t.Errorf("api version = %q", mresp.GetApiVersion())
	}
}

func TestFlagChange_JSONShape(t *testing.T) {
	b, _ := json.Marshal(FlagChange{Flag: "checkout", Environment: "prod", Percentage: 50})
	var c FlagChange
	if err := json.Unmarshal(b, &c); err != nil || c.Flag != "checkout" || c.Percentage != 50 {
		t.Fatalf("round trip = %+v err=%v", c, err)
	}
}
