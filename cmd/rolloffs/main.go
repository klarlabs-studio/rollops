// Command rolloffs is the Rolloffs CLI. It runs against the same engine
// library in two modes: in-process (one-shot, no daemon required — good for
// local use, CI, and recovery) or as a gRPC client talking to a running
// daemon. The command surface is identical across both modes.
package main

import (
	"fmt"
	"os"
)

func main() {
	// Scaffold entrypoint. Command tree (plan/apply/verify/promote/rollback/
	// status/schedule) is wired in a later task — see the Roady plan.
	fmt.Fprintln(os.Stderr, "rolloffs: scaffold — not yet implemented")
	os.Exit(1)
}
