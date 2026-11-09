package dream

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"valinor/internal/logger"
)

// runReport is the aggregate record of one dreaming attempt.
type runReport struct {
	Status           string
	SessionIDs       []string
	MemoryBefore     int
	MemoryAfter      int
	HeadingsAdded    int
	HeadingsRemoved  int
	PromptTokens     int64
	CompletionTokens int64
	Duration         time.Duration
	BackupPath       string
	Error            string
}

// writeJournal writes an aggregate dream report to the workspace and updates latest.md.
func writeJournal(workspaceDir string, at time.Time, report runReport) (string, error) {
	dir := filepath.Join(workspaceDir, "memory", journalDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create journal dir: %w", err)
	}

	body := renderJournal(at, report)
	name := at.UTC().Format("20060102T150405Z") + ".md"
	path := filepath.Join(dir, name)
	if err := writeFileAtomic(path, []byte(body), 0o644); err != nil {
		return "", err
	}
	if err := writeFileAtomic(filepath.Join(dir, latestJournalFile), []byte(body), 0o644); err != nil {
		logger.Warning("dreaming latest journal write failed", logger.BuildMetadata("error", err))
	}
	if err := pruneFiles(dir, journalRetention, "", latestJournalFile); err != nil {
		logger.Warning("dreaming journal prune failed", logger.BuildMetadata("error", err))
	}
	return path, nil
}

func renderJournal(at time.Time, report runReport) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# Dream — %s\n\n", at.Format(time.RFC3339))
	fmt.Fprintf(&sb, "- Status: %s\n", report.Status)
	fmt.Fprintf(&sb, "- Sessions: %d", len(report.SessionIDs))
	if len(report.SessionIDs) > 0 {
		sb.WriteString(" (")
		sb.WriteString(strings.Join(report.SessionIDs, ", "))
		sb.WriteString(")")
	}
	sb.WriteString("\n")
	fmt.Fprintf(&sb, "- Memory: %d -> %d bytes\n", report.MemoryBefore, report.MemoryAfter)
	fmt.Fprintf(&sb, "- Headings: added %d, removed %d\n", report.HeadingsAdded, report.HeadingsRemoved)
	fmt.Fprintf(&sb, "- Tokens: prompt %d, completion %d\n", report.PromptTokens, report.CompletionTokens)
	fmt.Fprintf(&sb, "- Duration: %s\n", report.Duration.Round(time.Millisecond))
	if report.BackupPath != "" {
		fmt.Fprintf(&sb, "- Backup: %s\n", filepath.Base(report.BackupPath))
	}
	if report.Error != "" {
		fmt.Fprintf(&sb, "- Error: %s\n", report.Error)
	}
	return sb.String()
}

// backupMemory saves the current memory file and returns the backup path.
func backupMemory(stateDir string, data []byte, at time.Time) (string, error) {
	dir := backupsDir(stateDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	name := fmt.Sprintf("%s-%s.md", memoryBackupBase, at.UTC().Format("20060102T150405Z"))
	path := filepath.Join(dir, name)
	if err := writeFileAtomic(path, data, 0o600); err != nil {
		return "", err
	}
	if err := pruneFiles(dir, backupRetention, memoryBackupBase+"-", ""); err != nil {
		logger.Warning("dreaming backup prune failed", logger.BuildMetadata("error", err))
	}
	return path, nil
}

// pruneFiles removes all but the newest keep files with an optional name prefix.
func pruneFiles(dir string, keep int, prefix string, skip string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".md") || name == skip {
			continue
		}
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		names = append(names, name)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))

	for i := keep; i < len(names); i++ {
		if err := os.Remove(filepath.Join(dir, names[i])); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove old file %q: %w", names[i], err)
		}
	}
	return nil
}

// headingDelta counts headings added and removed between two memory snapshots.
func headingDelta(before, after string) (added, removed int) {
	beforeSet := make(map[string]struct{})
	for _, heading := range extractHeadings(before) {
		beforeSet[heading] = struct{}{}
	}
	afterSet := make(map[string]struct{})
	for _, heading := range extractHeadings(after) {
		afterSet[heading] = struct{}{}
	}
	for heading := range afterSet {
		if _, ok := beforeSet[heading]; !ok {
			added++
		}
	}
	for heading := range beforeSet {
		if _, ok := afterSet[heading]; !ok {
			removed++
		}
	}
	return added, removed
}
