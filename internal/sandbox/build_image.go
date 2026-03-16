package sandbox

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

var (
	//go:embed Dockerfile.sandbox
	sandboxDockerfile string
)

// BuildImage builds the sandbox image directly from the embedded Dockerfile.
func BuildImage(ctx context.Context, image string, stdout, stderr io.Writer) error {
	image = strings.TrimSpace(image)
	if image == "" {
		return fmt.Errorf("sandbox image name is required")
	}
	if err := checkPodmanInstalled(); err != nil {
		return err
	}

	buildContextDir, err := os.MkdirTemp("", "valinor-sandbox-build-*")
	if err != nil {
		return fmt.Errorf("create temporary build context: %w", err)
	}
	defer os.RemoveAll(buildContextDir)

	cmd := exec.CommandContext(ctx, "podman", "build",
		"-t", image,
		"-f", "-",
		buildContextDir,
	)
	cmd.Stdin = strings.NewReader(sandboxDockerfile)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build sandbox image %q: %w", image, err)
	}

	return nil
}
