package requiredskills

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"valinor/internal/logger"
)

const workspaceSkillsDir = "skills"

//go:embed all:web-research
var bundled embed.FS

// EnsureRequiredSkills writes embedded required skills into workspaceDir/skills/.
// Files are always written so package updates refresh required skills on startup.
func EnsureRequiredSkills(workspaceDir string) error {
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir == "" {
		return fmt.Errorf("workspace directory is required")
	}

	destRoot := filepath.Join(workspaceDir, workspaceSkillsDir)
	written := 0

	err := fs.WalkDir(bundled, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		data, err := bundled.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded required skill %q: %w", path, err)
		}

		target := filepath.Join(destRoot, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create skill parent dir %q: %w", filepath.Dir(target), err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return fmt.Errorf("write workspace skill %q: %w", target, err)
		}

		written++
		rel := filepath.ToSlash(filepath.Join(workspaceSkillsDir, path))
		logger.Debug("required skill written", logger.BuildMetadata("path", rel))
		return nil
	})
	if err != nil {
		return err
	}

	logger.Info("required skills ensured", logger.BuildMetadata("written_count", written, "workspace", workspaceDir))
	return nil
}
