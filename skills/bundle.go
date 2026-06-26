package skills

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

//go:embed all:*
var bundled embed.FS

// EnsureRequired writes embedded skill files into workspaceDir/skills/ when missing.
// Existing workspace skills are never overwritten.
func EnsureRequired(workspaceDir string) error {
	workspaceDir = strings.TrimSpace(workspaceDir)
	if workspaceDir == "" {
		return fmt.Errorf("workspace directory is required")
	}

	destRoot := filepath.Join(workspaceDir, workspaceSkillsDir)
	created := 0

	err := fs.WalkDir(bundled, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		data, err := bundled.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded skill %q: %w", path, err)
		}

		target := filepath.Join(destRoot, filepath.FromSlash(path))
		if _, err := os.Stat(target); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat workspace skill %q: %w", target, err)
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create skill parent dir %q: %w", filepath.Dir(target), err)
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return fmt.Errorf("write workspace skill %q: %w", target, err)
		}

		rel := filepath.ToSlash(filepath.Join(workspaceSkillsDir, path))
		created++
		logger.Info("required skill created", logger.BuildMetadata("path", rel))
		return nil
	})
	if err != nil {
		return err
	}

	if created > 0 {
		logger.Info("required skills ensured", logger.BuildMetadata("created_count", created, "workspace", workspaceDir))
	} else {
		logger.Debug("required skills already present", logger.BuildMetadata("workspace", workspaceDir))
	}

	return nil
}