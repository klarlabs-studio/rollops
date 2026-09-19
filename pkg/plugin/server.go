package plugin

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	"go.klarlabs.de/rollops/pkg/plugin/rollopspluginv1"
)

// ToolFunc handles one tool invocation: JSON in, JSON out.
type ToolFunc func(ctx context.Context, input []byte) ([]byte, error)

// Server implements the generic Plugin gRPC service from a manifest plus a set
// of registered tool handlers, keyed by "capability/tool". It may also carry
// typed contract services, which share the plugin's socket.
type Server struct {
	rollopspluginv1.UnimplementedPluginServer
	manifest  Manifest
	tools     map[string]ToolFunc
	contracts []func(grpc.ServiceRegistrar)
}

// NewServer builds a Server for the given manifest. Register handlers with
// HandleTool, then pass it to Serve.
func NewServer(m Manifest) *Server {
	return &Server{manifest: m, tools: make(map[string]ToolFunc)}
}

// HandleTool registers the handler for capability/tool. It returns the Server
// for chaining.
func (s *Server) HandleTool(capability, tool string, fn ToolFunc) *Server {
	s.tools[capability+"/"+tool] = fn
	return s
}

// ServeContract attaches a typed service and declares it in the same move, so
// the two cannot drift. A host looks for a typed service only where the
// manifest says one is, so a service registered without a declaration is one
// nobody calls, and a declaration without a service is a promise the plugin
// breaks on the first request.
func (s *Server) ServeContract(kind string, version int, register func(grpc.ServiceRegistrar)) *Server {
	s.manifest.Contracts = append(s.manifest.Contracts, DeclaredContract{Kind: kind, Version: version})
	s.contracts = append(s.contracts, register)
	return s
}

// RegisterContracts attaches every declared contract service to r. Serve calls
// it; a plugin that builds its own gRPC server calls it instead.
func (s *Server) RegisterContracts(r grpc.ServiceRegistrar) {
	for _, register := range s.contracts {
		register(r)
	}
}

// GetManifest returns the plugin manifest as proto.
func (s *Server) GetManifest(_ context.Context, _ *rollopspluginv1.GetManifestRequest) (*rollopspluginv1.GetManifestResponse, error) {
	return manifestToProto(s.manifest), nil
}

// InvokeTool routes to the registered handler.
func (s *Server) InvokeTool(ctx context.Context, req *rollopspluginv1.InvokeToolRequest) (*rollopspluginv1.InvokeToolResponse, error) {
	key := req.GetCapability() + "/" + req.GetTool()
	fn, ok := s.tools[key]
	if !ok {
		return nil, fmt.Errorf("plugin: unknown tool %q", key)
	}
	out, err := fn(ctx, req.GetInput())
	if err != nil {
		return nil, err
	}
	return &rollopspluginv1.InvokeToolResponse{Output: out}, nil
}

func manifestToProto(m Manifest) *rollopspluginv1.GetManifestResponse {
	caps := make([]*rollopspluginv1.Capability, 0, len(m.Capabilities))
	for _, c := range m.Capabilities {
		tools := make([]*rollopspluginv1.ToolDef, 0, len(c.Tools))
		for _, t := range c.Tools {
			tools = append(tools, &rollopspluginv1.ToolDef{Name: t.Name, Description: t.Description, Mutating: t.Mutating, RiskClass: string(t.RiskClass)})
		}
		caps = append(caps, &rollopspluginv1.Capability{Name: c.Name, Description: c.Description, Tools: tools})
	}
	contracts := make([]*rollopspluginv1.DeclaredContract, 0, len(m.Contracts))
	for _, c := range m.Contracts {
		contracts = append(contracts, &rollopspluginv1.DeclaredContract{Kind: c.Kind, Version: int32(c.Version)}) //nolint:gosec // a contract version is a small integer
	}
	return &rollopspluginv1.GetManifestResponse{
		Name:         m.Name,
		Version:      m.Version,
		ApiVersion:   APIVersion,
		Capabilities: caps,
		Contracts:    contracts,
		Safety: &rollopspluginv1.SafetyRequirements{
			NetworkHosts:      m.Safety.NetworkHosts,
			FilePaths:         m.Safety.FilePaths,
			EnvVars:           m.Safety.EnvVars,
			RiskClass:         string(m.Safety.RiskClass),
			NeedsConfirmation: m.Safety.NeedsConfirmation,
		},
	}
}
