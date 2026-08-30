//go:build unix

package procgroup

import (
	"fmt"
	"io"
)

// fscanPid reads the single integer the test's shell prints.
func fscanPid(r io.Reader, out *int) (int, error) { return fmt.Fscan(r, out) }
