// Package version holds build information injected at link time.
package version

import "runtime"

var (
	// Version is the semantic version or git describe output.
	Version = "dev"
	// Commit is the short git commit hash.
	Commit = "none"
	// Date is the UTC build timestamp.
	Date = "unknown"
)

// String renders the build information on one line.
func String() string {
	return Version + " (" + Commit + ", " + Date + ", " + runtime.Version() + ", " + runtime.GOOS + "/" + runtime.GOARCH + ")"
}
