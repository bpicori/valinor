package agentcontext

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"valinor/internal/logger"
)

//go:embed MEMORY.md
var embeddedMemoryTemplate string

// EnsureMemory writes the embedded MEMORY.md template into workspaceDir when missing.
// Existing workspace memory files are never overwritten.
func EnsureMemory(workspaceDir string) error {
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir == "" {
		return fmt.Errorf("workspace directory is required")
	}

	target := filepath.Join(workspaceDir, MemoryFile)
	if _, err := os.Stat(target); err == nil {
		logger.Debug("memory file already present", logger.BuildMetadata("path", MemoryFile, "workspace", workspaceDir))
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat workspace memory file %q: %w", target, err)
	}

	content := strings.TrimRight(embeddedMemoryTemplate, "\n") + "\n"
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write workspace memory file %q: %w", target, err)
	}

	logger.Info("memory file created", logger.BuildMetadata("path", MemoryFile, "workspace", workspaceDir))
	return nil
}