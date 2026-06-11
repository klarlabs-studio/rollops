package plugin

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"

	"google.golang.org/grpc"

	"go.klarlabs.de/rollops/pkg/plugin/rollopspluginv1"
	pt "go.klarlabs.de/rollops/pkg/target"
)

// Serve runs t as a Rollops target plugin: it listens on a private unix
// socket, prints the handshake line on stdout, and serves until stdin closes
// (the host exited or released the plugin) or the gRPC server fails. Call it
// from the plugin's main and treat a non-nil error as fatal.
func Serve(t pt.Target) error {
	dir, err := os.MkdirTemp("", "rollops-plugin-")
	if err != nil {
		return fmt.Errorf("plugin: serve: %w", err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "plugin.sock")

	lis, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("plugin: serve: %w", err)
	}
	srv := grpc.NewServer()
	rollopspluginv1.RegisterTargetPluginServer(srv, server{t: t})

	fmt.Println(Handshake{ProtocolVersion: ProtocolVersion, Cookie: Cookie, Addr: sock}.Line())

	// Parent-death watch: the host holds our stdin open; EOF means it is gone
	// (or closed us deliberately), so shut down instead of leaking.
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		srv.GracefulStop()
	}()

	if err := srv.Serve(lis); err != nil && err != grpc.ErrServerStopped {
		return fmt.Errorf("plugin: serve: %w", err)
	}
	return nil
}

// server adapts pkg/target.Target onto the generated gRPC service.
type server struct {
	rollopspluginv1.UnimplementedTargetPluginServer
	t pt.Target
}

func (s server) Apply(ctx context.Context, req *rollopspluginv1.ApplyRequest) (*rollopspluginv1.ApplyResponse, error) {
	res, err := s.t.Apply(ctx, pt.Manifest{Kind: req.GetKind(), Spec: req.GetSpec(), Checksum: req.GetChecksum()})
	if err != nil {
		return nil, err
	}
	return &rollopspluginv1.ApplyResponse{Changed: res.Changed, Detail: res.Detail}, nil
}

func (s server) Observe(ctx context.Context, _ *rollopspluginv1.ObserveRequest) (*rollopspluginv1.ObserveResponse, error) {
	fp, err := s.t.Observe(ctx)
	if err != nil {
		return nil, err
	}
	return &rollopspluginv1.ObserveResponse{Value: fp.Value, Meta: fp.Meta}, nil
}

func (s server) Health(ctx context.Context, _ *rollopspluginv1.HealthRequest) (*rollopspluginv1.HealthResponse, error) {
	hs, err := s.t.Health(ctx)
	if err != nil {
		return nil, err
	}
	return &rollopspluginv1.HealthResponse{State: int32(hs.State), Reason: hs.Reason}, nil
}
