package agentcontext

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"valinor/internal/logger"
)

//go:embed IDENTITY.md
var embeddedIdentityTemplate string

// EnsureIdentity writes the embedded IDENTITY.md template into workspaceDir when missing.
// Existing workspace identity files are never overwritten.
func EnsureIdentity(workspaceDir string) error {
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir == "" {
		return fmt.Errorf("workspace directory is required")
	}

	target := filepath.Join(workspaceDir, IdentityFile)
	if _, err := os.Stat(target); err == nil {
		logger.Debug("identity file already present", logger.BuildMetadata("path", IdentityFile, "workspace", workspaceDir))
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat workspace identity file %q: %w", target, err)
	}

	content := strings.TrimRight(embeddedIdentityTemplate, "\n") + "\n"
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write workspace identity file %q: %w", target, err)
	}

	logger.Info("identity file created", logger.BuildMetadata("path", IdentityFile, "workspace", workspaceDir))
	return nil
}