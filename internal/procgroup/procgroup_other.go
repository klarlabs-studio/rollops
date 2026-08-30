//go:build !unix

package procgroup

import "os/exec"

// Isolate is a no-op where process groups are not available.
func Isolate(*exec.Cmd) {}

// Kill is a no-op where process groups are not available; the standard library's
// own cancellation still signals the direct child.
func Kill(int) {}
