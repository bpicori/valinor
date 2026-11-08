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

// staleMemoryHints are tool names removed in favor of direct file edits.
// Older seeded workspaces still tell the model to call them.
var staleMemoryHints = []string{"memory_update", "memory_log", "memory_search"}

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
		warnIfStaleMemoryTemplate(target)
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

// warnIfStaleMemoryTemplate reports a user-owned MEMORY.md that still points at
// removed memory tools. The file is never rewritten automatically.
func warnIfStaleMemoryTemplate(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, tool := range staleMemoryHints {
		if strings.Contains(string(data), tool) {
			logger.Warning("workspace MEMORY.md references removed memory tools",
				logger.BuildMetadata(
					"path", path,
					"tool", tool,
					"hint", "MEMORY.md is user-owned and not rewritten; edit it to drop references to removed memory tools",
				))
			return
		}
	}
}