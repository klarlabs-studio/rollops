//go:build !unix

package pluginhost

import "os/exec"

// isolateProcess is a no-op on platforms without process groups.
func isolateProcess(*exec.Cmd) {}

// killProcessGroup falls back to killing the single process.
func killProcessGroup(int) {}
