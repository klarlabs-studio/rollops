package pluginhost

import (
	"context"
	"fmt"

	pub "go.klarlabs.de/rollops/pkg/plugin"
	"go.klarlabs.de/rollops/pkg/plugin/rollopspluginv1"
)

// Client is the host's generic view of a launched plugin.
type Client struct {
	rpc rollopspluginv1.PluginClient
}

// Manifest fetches and converts the plugin's manifest.
func (c *Client) Manifest(ctx context.Context) (pub.Manifest, error) {
	resp, err := c.rpc.GetManifest(ctx, &rollopspluginv1.GetManifestRequest{ApiVersion: pub.APIVersion})
	if err != nil {
		return pub.Manifest{}, fmt.Errorf("plugin: get manifest: %w", err)
	}
	m := pub.Manifest{Name: resp.GetName(), Version: resp.GetVersion()}
	for _, c := range resp.GetCapabilities() {
		cap := pub.Capability{Name: c.GetName(), Description: c.GetDescription()}
		for _, t := range c.GetTools() {
			cap.Tools = append(cap.Tools, pub.Tool{Name: t.GetName(), Description: t.GetDescription(), Mutating: t.GetMutating(), RiskClass: pub.RiskClass(t.GetRiskClass())})
		}
		m.Capabilities = append(m.Capabilities, cap)
	}
	if s := resp.GetSafety(); s != nil {
		m.Safety = pub.Safety{
			NetworkHosts:      s.GetNetworkHosts(),
			FilePaths:         s.GetFilePaths(),
			EnvVars:           s.GetEnvVars(),
			RiskClass:         pub.RiskClass(s.GetRiskClass()),
			NeedsConfirmation: s.GetNeedsConfirmation(),
		}
	}
	return m, nil
}

// Invoke calls a tool with JSON input and returns its JSON output.
func (c *Client) Invoke(ctx context.Context, capability, tool string, input []byte) ([]byte, error) {
	resp, err := c.rpc.InvokeTool(ctx, &rollopspluginv1.InvokeToolRequest{Capability: capability, Tool: tool, Input: input})
	if err != nil {
		return nil, fmt.Errorf("plugin: invoke %s/%s: %w", capability, tool, err)
	}
	return resp.GetOutput(), nil
}

// HasCapability reports whether the manifest declares the named capability.
func HasCapability(m pub.Manifest, name string) bool {
	for _, c := range m.Capabilities {
		if c.Name == name {
			return true
		}
	}
	return false
}
