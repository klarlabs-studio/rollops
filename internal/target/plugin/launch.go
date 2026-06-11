package plugin

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pubplugin "go.klarlabs.de/rollops/pkg/plugin"
	"go.klarlabs.de/rollops/pkg/plugin/rollopspluginv1"
)

// handshakeTimeout bounds how long a plugin may take to print its handshake.
const handshakeTimeout = 10 * time.Second

// VerifyBinary compares the file's sha256 against the pinned hex digest. The
// pin is required: an unpinned plugin binary is a supply-chain hole.
func VerifyBinary(path, sha256hex string) error {
	if sha256hex == "" {
		return fmt.Errorf("plugin: binary %s: sha256 pin required", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("plugin: open binary: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("plugin: hash binary: %w", err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, sha256hex) {
		return fmt.Errorf("plugin: binary %s: sha256 mismatch: pinned %s, got %s", path, sha256hex, got)
	}
	return nil
}

// Process is a running plugin subprocess with an established RPC. Close shuts
// the plugin down (stdin close → graceful stop, then kill as backstop).
type Process struct {
	Target *Target
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	conn   *grpc.ClientConn
}

// Close releases the plugin: connection, stdin (its shutdown signal), and the
// process itself.
func (p *Process) Close() error {
	if p.conn != nil {
		_ = p.conn.Close()
	}
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		done := make(chan struct{})
		go func() { _, _ = p.cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = p.cmd.Process.Kill()
		}
	}
	return nil
}

// Launch starts the plugin binary, verifies its handshake line, dials its gRPC
// server, and returns the running process with an adapted Target.
func Launch(ctx context.Context, path string) (*Process, error) {
	cmd := exec.CommandContext(ctx, path)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin: launch: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin: launch: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("plugin: launch %s: %w", path, err)
	}

	hs, err := awaitHandshake(stdout)
	if err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		return nil, err
	}
	if err := hs.Verify(); err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		return nil, err
	}
	// Drain remaining stdout so the plugin never blocks on a full pipe.
	go func() { _, _ = io.Copy(io.Discard, stdout) }()

	conn, err := grpc.NewClient("unix://"+hs.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("plugin: dial %s: %w", hs.Addr, err)
	}
	rpc := grpcRPC{c: rollopspluginv1.NewTargetPluginClient(conn)}
	return &Process{Target: NewTarget(rpc), cmd: cmd, stdin: stdin, conn: conn}, nil
}

// awaitHandshake scans stdout lines for the handshake, skipping plugin log
// output, bounded by handshakeTimeout.
func awaitHandshake(r io.Reader) (pubplugin.Handshake, error) {
	type result struct {
		hs  pubplugin.Handshake
		err error
	}
	ch := make(chan result, 1)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if hs, ok := pubplugin.ParseHandshake(sc.Text()); ok {
				ch <- result{hs: hs}
				return
			}
		}
		ch <- result{err: fmt.Errorf("plugin: exited before handshake")}
	}()
	select {
	case res := <-ch:
		return res.hs, res.err
	case <-time.After(handshakeTimeout):
		return pubplugin.Handshake{}, fmt.Errorf("plugin: handshake timeout after %s", handshakeTimeout)
	}
}

// grpcRPC adapts the generated gRPC client onto the RPC seam.
type grpcRPC struct {
	c rollopspluginv1.TargetPluginClient
}

func (g grpcRPC) Apply(ctx context.Context, kind string, spec []byte, checksum string) (bool, string, error) {
	res, err := g.c.Apply(ctx, &rollopspluginv1.ApplyRequest{Kind: kind, Spec: spec, Checksum: checksum})
	if err != nil {
		return false, "", err
	}
	return res.GetChanged(), res.GetDetail(), nil
}

func (g grpcRPC) Observe(ctx context.Context) (string, map[string]string, error) {
	res, err := g.c.Observe(ctx, &rollopspluginv1.ObserveRequest{})
	if err != nil {
		return "", nil, err
	}
	return res.GetValue(), res.GetMeta(), nil
}

func (g grpcRPC) Health(ctx context.Context) (int, string, error) {
	res, err := g.c.Health(ctx, &rollopspluginv1.HealthRequest{})
	if err != nil {
		return 0, "", err
	}
	return int(res.GetState()), res.GetReason(), nil
}
