package version

import "fmt"

var (
	// These values are injected at build time by the release workflow.
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// String returns the current build metadata in a single line.
func String() string {
	return fmt.Sprintf("version=%s commit=%s build_date=%s", Version, Commit, BuildDate)
}
