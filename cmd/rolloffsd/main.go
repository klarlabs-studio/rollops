// Command rolloffsd is the Rolloffs daemon: the always-on reconciler that
// watches Git, detects drift, fires scheduled rollouts, and wraps the shared
// engine library behind gRPC + a grpc-gateway REST front, with the MCP server
// embedded by default.
package main

import (
	"fmt"
	"os"
)

func main() {
	// Scaffold entrypoint. Reconciler loop, gRPC/REST serving, and embedded
	// MCP are wired in later tasks — see the Roady plan.
	fmt.Fprintln(os.Stderr, "rolloffsd: scaffold — not yet implemented")
	os.Exit(1)
}
