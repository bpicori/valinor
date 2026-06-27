package sandbox

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"valinor/internal/buildinfo"
)

var (
	//go:embed Dockerfile.sandbox
	sandboxDockerfile string
	//go:embed searxng-settings.yml
	searxngSettings string
	//go:embed entrypoint.sh
	sandboxEntrypoint string
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

	if err := os.WriteFile(filepath.Join(buildContextDir, "searxng-settings.yml"), []byte(searxngSettings), 0o644); err != nil {
		return fmt.Errorf("write searxng settings: %w", err)
	}
	if err := os.WriteFile(filepath.Join(buildContextDir, "entrypoint.sh"), []byte(sandboxEntrypoint), 0o755); err != nil {
		return fmt.Errorf("write entrypoint: %w", err)
	}

	cmd := exec.CommandContext(ctx, "podman", "build",
		"-t", image,
		"--label", "valinor.version="+buildinfo.Version,
		"--label", "valinor.commit="+buildinfo.Commit,
		"--label", "valinor.digest="+sandboxBuildDigest(),
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

func sandboxBuildDigest() string {
	sum := sha256.Sum256([]byte(sandboxDockerfile + "\x00" + searxngSettings + "\x00" + sandboxEntrypoint))
	return hex.EncodeToString(sum[:16])
}