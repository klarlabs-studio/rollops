package plugin

import "go.klarlabs.de/rollops/pkg/plugin/rollopspluginv1"

func invoke(cap, tool string, in []byte) *rollopspluginv1.InvokeToolRequest {
	return &rollopspluginv1.InvokeToolRequest{Capability: cap, Tool: tool, Input: in}
}
