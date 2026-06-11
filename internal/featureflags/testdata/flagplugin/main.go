// Command flagplugin is a minimal feature-flag plugin used by the host's
// end-to-end test: it records the last flag change to a file so the test can
// assert the full subprocess + gRPC path delivered it.
package main

import (
	"context"
	"fmt"
	"os"

	"go.klarlabs.de/rollops/pkg/plugin"
)

type recorder struct{ path string }

func (r recorder) ApplyFlag(_ context.Context, c plugin.FlagChange) error {
	line := fmt.Sprintf("%s=%d@%s\n", c.Flag, c.Percentage, c.Environment)
	f, err := os.OpenFile(r.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line)
	return err
}

func main() {
	rec := recorder{path: os.Getenv("ROLLOPS_FLAG_OUTFILE")}
	if err := plugin.ServeFlagProvider("rollops/testflag", "1.0.0", rec, plugin.Safety{RiskClass: plugin.RiskActive}); err != nil {
		fmt.Fprintln(os.Stderr, "flagplugin:", err)
		os.Exit(1)
	}
}
