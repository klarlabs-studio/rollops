// Package pluginhost is the host side of the Rollops plugin runtime: it
// launches a sha256-pinned plugin binary as a subprocess, completes the
// handshake, dials its gRPC server, fetches and safety-validates the manifest,
// and exposes generic tool invocation. Capability adapters (target,
// feature-flag) build typed surfaces on top of a *Client. The protocol is the
// generic manifest+InvokeTool service (pkg/plugin), modeled on nox-hq.
package pluginhost

import (
	"bufio"
	"bytes"
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

const (
	handshakeTimeout = 10 * time.Second
	maxHandshakeLine = 64 * 1024
	maxStderrBytes   = 1 << 20 // 1 MiB
)

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
	defer func() { _ = f.Close() }()
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

// Process is a running plugin subprocess with an established Client.
type Process struct {
	Client *Client
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	conn   *grpc.ClientConn
}

// Close releases the plugin: connection, stdin (its shutdown signal), and the
// process group (so forked children leave no orphans).
func (p *Process) Close() error {
	if p.conn != nil {
		_ = p.conn.Close()
	}
	if p.stdin != nil {
		_ = p.stdin.Close()
	}
	if p.cmd != nil && p.cmd.Process != nil {
		pid := p.cmd.Process.Pid
		done := make(chan struct{})
		go func() { _, _ = p.cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			_ = p.cmd.Process.Kill()
			<-done
		}
		killProcessGroup(pid)
	}
	return nil
}

// Launch starts the plugin binary, verifies its handshake, dials its gRPC
// server, and returns the running process with a generic Client.
func Launch(ctx context.Context, path string) (*Process, error) {
	cmd := exec.CommandContext(ctx, path)
	isolateProcess(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin: launch: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("plugin: launch: %w", err)
	}
	cmd.Stderr = &cappedWriter{w: os.Stderr, prefix: []byte("[plugin] "), remaining: maxStderrBytes}
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
	go func() { _, _ = io.Copy(io.Discard, stdout) }()

	conn, err := grpc.NewClient("unix://"+hs.Addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("plugin: dial %s: %w", hs.Addr, err)
	}
	return &Process{
		Client: &Client{rpc: rollopspluginv1.NewPluginClient(conn)},
		cmd:    cmd, stdin: stdin, conn: conn,
	}, nil
}

func awaitHandshake(r io.Reader) (pubplugin.Handshake, error) {
	type result struct {
		hs  pubplugin.Handshake
		err error
	}
	ch := make(chan result, 1)
	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 4096), maxHandshakeLine)
		for sc.Scan() {
			if hs, ok := pubplugin.ParseHandshake(sc.Text()); ok {
				ch <- result{hs: hs}
				return
			}
		}
		if err := sc.Err(); err != nil {
			ch <- result{err: fmt.Errorf("plugin: reading handshake: %w", err)}
			return
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

// cappedWriter relays at most `remaining` bytes, prefixing each line, then
// drops the rest. Not safe for concurrent writers; cmd.Stderr is single-writer.
type cappedWriter struct {
	w         io.Writer
	prefix    []byte
	midLine   bool
	remaining int
	truncated bool
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		if c.remaining <= 0 {
			if !c.truncated {
				c.truncated = true
				_, _ = c.w.Write([]byte("[plugin] … stderr truncated\n"))
			}
			return n, nil
		}
		if !c.midLine {
			_, _ = c.w.Write(c.prefix)
		}
		var chunk []byte
		if i := bytes.IndexByte(p, '\n'); i < 0 {
			chunk, p = p, nil
			c.midLine = true
		} else {
			chunk, p = p[:i+1], p[i+1:]
			c.midLine = false
		}
		if len(chunk) > c.remaining {
			chunk = chunk[:c.remaining]
		}
		c.remaining -= len(chunk)
		_, _ = c.w.Write(chunk)
	}
	return n, nil
}
