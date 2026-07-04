//go:build !linux && !darwin

package deploy

import (
	"context"
	"fmt"
)

func platformName() string { return "unsupported" }

func platformUninstall(context.Context, Paths) error {
	return unsupportedPlatform()
}

func platformStatus(context.Context, Paths) (Status, error) {
	return Status{}, unsupportedPlatform()
}

func platformRestart(context.Context, Paths) error {
	return unsupportedPlatform()
}

func platformRestartIfInstalled(context.Context, Paths) error {
	return unsupportedPlatform()
}

func platformLogs(context.Context, Paths, LogOptions) error {
	return unsupportedPlatform()
}

func unsupportedPlatform() error {
	return fmt.Errorf("deploy commands are supported only on linux and darwin")
}