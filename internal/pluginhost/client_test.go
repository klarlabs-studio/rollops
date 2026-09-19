package pluginhost

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"

	"go.klarlabs.de/rollops/pkg/plugin/rollopspluginv1"
)

// stallClient is a PluginClient whose GetManifest never returns on its own — it
// blocks until the caller's context is cancelled, standing in for a plugin that
// hangs the manifest RPC.
type stallClient struct{}

func (stallClient) GetManifest(ctx context.Context, _ *rollopspluginv1.GetManifestRequest, _ ...grpc.CallOption) (*rollopspluginv1.GetManifestResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (stallClient) InvokeTool(ctx context.Context, _ *rollopspluginv1.InvokeToolRequest, _ ...grpc.CallOption) (*rollopspluginv1.InvokeToolResponse, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// cannedClient answers GetManifest with a fixed response.
type cannedClient struct {
	stallClient
	res *rollopspluginv1.GetManifestResponse
}

func (c cannedClient) GetManifest(context.Context, *rollopspluginv1.GetManifestRequest, ...grpc.CallOption) (*rollopspluginv1.GetManifestResponse, error) {
	return c.res, nil
}

// TestADeclaredContractSurvivesTheManifestRead is what makes a typed service
// reachable at all: the host looks for one only where the manifest says it is.
func TestADeclaredContractSurvivesTheManifestRead(t *testing.T) {
	c := &Client{rpc: cannedClient{res: &rollopspluginv1.GetManifestResponse{
		Name:    "acme/exotic",
		Version: "1.0.0",
		Contracts: []*rollopspluginv1.DeclaredContract{
			{Kind: "target", Version: 2},
		},
	}}}

	m, err := c.Manifest(context.Background())
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	v, ok := m.Contract("target")
	if !ok {
		t.Fatal("the declared target contract did not survive the read")
	}
	if v != 2 {
		t.Errorf("target contract version %d, want 2", v)
	}
	if _, ok := m.Contract("featureflag"); ok {
		t.Error("a contract the plugin never declared was reported as declared")
	}
}

// TestAPluginFromBeforeContractsIsNotBroken covers the older plugin. It sends
// no contracts at all, which must read as "serves none" rather than as a
// malformed manifest — that is the point of not bumping the protocol version.
func TestAPluginFromBeforeContractsIsNotBroken(t *testing.T) {
	c := &Client{rpc: cannedClient{res: &rollopspluginv1.GetManifestResponse{Name: "acme/old"}}}

	m, err := c.Manifest(context.Background())
	if err != nil {
		t.Fatalf("Manifest: %v", err)
	}
	if _, ok := m.Contract("target"); ok {
		t.Error("a plugin that declared nothing was read as serving the target contract")
	}
}

// TestClientManifest_HonoursDeadline ensures the manifest call is bounded by its
// context: a stalling plugin must not hang the caller. This is the invariant the
// adapters rely on when they wrap Manifest in context.WithTimeout(ManifestTimeout).
func TestClientManifest_HonoursDeadline(t *testing.T) {
	c := &Client{rpc: stallClient{}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := c.Manifest(ctx)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled manifest call must return an error, not succeed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Manifest did not respect the context deadline (unbounded call)")
	}
}
