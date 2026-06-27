package sandbox

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"valinor/internal/buildinfo"
	"valinor/internal/logger"
)

const (
	sandboxBuildTimeout = 45 * time.Minute
	podmanQuickTimeout  = 5 * time.Second
)

// ensureSandboxImageReady builds the sandbox image when it is missing or stale.
func ensureSandboxImageReady(image string) error {
	checkCtx, checkCancel := context.WithTimeout(context.Background(), podmanQuickTimeout)
	defer checkCancel()

	exists := exec.CommandContext(checkCtx, "podman", "image", "exists", image).Run() == nil

	upToDate := false
	if exists {
		version, err := imageLabel(checkCtx, image, "valinor.version")
		if err != nil {
			return err
		}
		digest, err := imageLabel(checkCtx, image, "valinor.digest")
		if err != nil {
			return err
		}
		upToDate = version != "" && digest != "" &&
			version == buildinfo.Version && digest == sandboxBuildDigest()
	}

	if exists && upToDate {
		logger.Debug("sandbox image is ready", logger.BuildMetadata("image", image))
		return nil
	}

	meta := logger.BuildMetadata("image", image, "valinor_version", buildinfo.Version)
	if exists {
		logger.Info("sandbox image is outdated, rebuilding", meta)
		fmt.Fprintf(os.Stderr, "Sandbox image %q is outdated for Valinor %s. Rebuilding...\n", image, buildinfo.Version)
	} else {
		logger.Info("sandbox image not found, building", meta)
		fmt.Fprintf(os.Stderr, "Sandbox image %q not found for Valinor %s. Building...\n", image, buildinfo.Version)
	}

	buildCtx, buildCancel := context.WithTimeout(context.Background(), sandboxBuildTimeout)
	defer buildCancel()

	if err := BuildImage(buildCtx, image, os.Stdout, os.Stderr); err != nil {
		return fmt.Errorf("build sandbox image %q: %w", image, err)
	}

	logger.Info("sandbox image built", meta)
	fmt.Fprintf(os.Stderr, "Sandbox image %q built successfully.\n", image)
	return nil
}

func imageLabel(ctx context.Context, image, key string) (string, error) {
	format := fmt.Sprintf(`{{index .Labels %q}}`, key)
	out, err := exec.CommandContext(ctx, "podman", "image", "inspect", "--format", format, image).Output()
	if err != nil {
		return "", fmt.Errorf("inspect sandbox image %q label %q: %w", image, key, err)
	}
	return strings.TrimSpace(string(out)), nil
}