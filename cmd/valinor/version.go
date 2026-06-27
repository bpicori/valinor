package main

import (
	"fmt"

	"valinor/internal/buildinfo"
)

func versionString() string {
	return fmt.Sprintf("version=%s commit=%s build_date=%s", buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate)
}