package version

import "fmt"

// These values are overridden by the Makefile with -ldflags for release builds.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// String returns the compact human-readable build identity.
func String() string {
	return fmt.Sprintf("rollops %s (%s, %s)", Version, Commit, Date)
}
