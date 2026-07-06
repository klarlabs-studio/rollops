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
