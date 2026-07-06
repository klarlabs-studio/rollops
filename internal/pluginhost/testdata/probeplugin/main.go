// Command probeplugin is a host-side test helper for the launcher, with two
// modes selected by environment variable:
//
//   - PROBE_ENV_DUMP_FILE: write the plugin's full environment to that file,
//     then serve a trivial plugin so the launcher succeeds. The env-confinement
//     test reads the file and asserts the daemon's secrets are absent.
//   - PROBE_FORK_PIDFILE: fork a long-lived child, record its pid, then emit a
//     well-formed but wrong-cookie handshake so Launch fails after start. The
//     group-kill test asserts the forked child is reaped, not orphaned.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"go.klarlabs.de/rollops/pkg/plugin"
)

func main() {
	// Group-kill mode: fork a child, then fail the handshake on purpose.
	if pf := os.Getenv("PROBE_FORK_PIDFILE"); pf != "" {
		child := exec.Command("sleep", "300")
		if err := child.Start(); err == nil {
			_ = os.WriteFile(pf, []byte(strconv.Itoa(child.Process.Pid)), 0o600)
		}
		// A parseable handshake with the wrong cookie: Launch reads it, Verify
		// rejects it, and the handshake-failure path must kill the whole group.
		fmt.Println(plugin.Handshake{ProtocolVersion: plugin.ProtocolVersion, Cookie: "WRONG-COOKIE", Addr: "/tmp/probeplugin-nope.sock"}.Line())
		select {} // stay alive until the launcher kills the group
	}

	// Env-confinement mode: record what environment the plugin actually sees.
	if df := os.Getenv("PROBE_ENV_DUMP_FILE"); df != "" {
		_ = os.WriteFile(df, []byte(strings.Join(os.Environ(), "\n")), 0o600)
	}

	m := plugin.NewManifest("rollops/probeplugin", "1.0.0").
		Capability("probe", "Test probe capability").
		Tool("noop", "Do nothing", false).
		Done().
		Safety(plugin.Safety{RiskClass: plugin.RiskPassive}).
		Build()
	srv := plugin.NewServer(m).HandleTool("probe", "noop", func(context.Context, []byte) ([]byte, error) {
		return []byte("{}"), nil
	})
	if err := plugin.Serve(srv); err != nil {
		fmt.Fprintln(os.Stderr, "probeplugin:", err)
		os.Exit(1)
	}
}
