package plugin

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"

	"google.golang.org/grpc"

	"go.klarlabs.de/rollops/pkg/plugin/rollopspluginv1"
)

// Serve runs srv as a Rollops plugin: it listens on a private unix socket,
// prints the handshake line on stdout, and serves until stdin closes (the host
// released the plugin) or the gRPC server fails. Call it from the plugin's main
// and treat a non-nil error as fatal.
func Serve(srv *Server) error {
	dir, err := os.MkdirTemp("", "rollops-plugin-")
	if err != nil {
		return fmt.Errorf("plugin: serve: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	sock := filepath.Join(dir, "plugin.sock")

	lis, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("plugin: serve: %w", err)
	}
	g := grpc.NewServer()
	rollopspluginv1.RegisterPluginServer(g, srv)

	fmt.Println(Handshake{ProtocolVersion: ProtocolVersion, Cookie: Cookie, Addr: sock}.Line())

	// Parent-death watch: the host holds our stdin open; EOF means it is gone
	// (or closed us deliberately), so shut down instead of leaking.
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		g.GracefulStop()
	}()

	if err := g.Serve(lis); err != nil && err != grpc.ErrServerStopped {
		return fmt.Errorf("plugin: serve: %w", err)
	}
	return nil
}
