package main

import "fmt"

var (
	// These values are injected at build time by the release workflow.
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

func versionString() string {
	return fmt.Sprintf("version=%s commit=%s build_date=%s", Version, Commit, BuildDate)
}