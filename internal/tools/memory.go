package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"valinor/internal/sandbox"
)

const (
	memoryLogName           = "memory_log"
	memoryUpdateName        = "memory_update"
	memorySearchName        = "memory_search"
	memoryFile              = "MEMORY.md"
	memoryDailyDir          = "memory/daily"
	memorySearchDefaultDays = 30
	memorySearchMaxDays     = 365
	memorySearchMaxChars    = 8000
)

func memoryLogParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]string{
				"type":        "string",
				"description": "The memory entry to log",
			},
		},
		"required":             []string{"content"},
		"additionalProperties": false,
	}
}

func memoryLogExec(rt *sandbox.Runtime, args json.RawMessage) (string, error) {
	var p struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}

	if p.Content == "" {
		return "Tool Error: content must not be empty", nil
	}

	today := time.Now().Format("2006-01-02")
	path := fmt.Sprintf("%s/%s.md", memoryDailyDir, today)

	existing, err := rt.ReadFile(path)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(existing, "Tool Error: ") {
		// File doesn't exist, treat as empty
		existing = ""
	}

	var combined string
	if existing == "" {
		// First entry of the day - no leading newline
		combined = fmt.Sprintf("- %s\n", p.Content)
	} else {
		combined = fmt.Sprintf("%s\n- %s\n", strings.TrimRight(existing, "\n"), p.Content)
	}

	_, err = rt.WriteFile(path, combined)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("ok: logged to %s", path), nil
}

func memoryUpdateParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"section": map[string]string{
				"type":        "string",
				"description": "Section header name (e.g. 'User Preferences'). Will be rendered as '## <section>' in the file",
			},
			"content": map[string]string{
				"type":        "string",
				"description": "The markdown content for this section",
			},
		},
		"required":             []string{"section", "content"},
		"additionalProperties": false,
	}
}

func memoryUpdateExec(rt *sandbox.Runtime, args json.RawMessage) (string, error) {
	var p struct {
		Section string `json:"section"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}

	if p.Section == "" {
		return "Tool Error: section must not be empty", nil
	}
	if p.Content == "" {
		return "Tool Error: content must not be empty", nil
	}

	existing, err := rt.ReadFile(memoryFile)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(existing, "Tool Error: ") {
		// File doesn't exist, treat as empty
		existing = ""
	}

	sectionHeader := fmt.Sprintf("## %s", p.Section)
	updated := upsertSection(existing, sectionHeader, p.Content)

	_, err = rt.WriteFile(memoryFile, updated)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("ok: updated section '%s' in MEMORY.md", p.Section), nil
}

func memorySearchParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]string{
				"type":        "string",
				"description": "Regular expression to search for in daily memory files",
			},
			"days": map[string]any{
				"type":        "integer",
				"description": "How many days back to search (default 30, max 365)",
			},
		},
		"required":             []string{"pattern"},
		"additionalProperties": false,
	}
}

func memorySearchExec(rt *sandbox.Runtime, args json.RawMessage) (string, error) {
	var p struct {
		Pattern string `json:"pattern"`
		Days    int    `json:"days"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Pattern) == "" {
		return "Tool Error: pattern must not be empty", nil
	}

	result, err := searchDailyMemories(rt.WorkspaceDir(), p.Pattern, p.Days)
	if err != nil {
		return "", err
	}
	return result, nil
}

func searchDailyMemories(workspaceDir, pattern string, maxDays int) (string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex: %w", err)
	}
	if maxDays <= 0 {
		maxDays = memorySearchDefaultDays
	}
	if maxDays > memorySearchMaxDays {
		maxDays = memorySearchMaxDays
	}

	dailyDir := filepath.Join(workspaceDir, memoryDailyDir)
	entries, err := os.ReadDir(dailyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "No daily memory files found.", nil
		}
		return "", fmt.Errorf("read daily memory dir: %w", err)
	}

	var files []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(strings.ToLower(name), ".md") {
			files = append(files, name)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(files)))

	cutoff := time.Now().AddDate(0, 0, -maxDays)
	var sb strings.Builder
	totalChars := 0

	for _, name := range files {
		dateName := strings.TrimSuffix(name, filepath.Ext(name))
		fileDate, err := time.Parse("2006-01-02", dateName)
		if err != nil {
			continue
		}
		if fileDate.Before(cutoff) {
			break
		}

		data, err := os.ReadFile(filepath.Join(dailyDir, name))
		if err != nil {
			continue
		}

		var dayMatches []string
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				dayMatches = append(dayMatches, fmt.Sprintf("  L%d: %s", i+1, strings.TrimRight(line, "\r")))
			}
		}
		if len(dayMatches) == 0 {
			continue
		}

		section := fmt.Sprintf("## %s\n%s\n", dateName, strings.Join(dayMatches, "\n"))
		if totalChars+len(section) > memorySearchMaxChars {
			sb.WriteString("\n... (truncated)")
			break
		}
		sb.WriteString(section)
		totalChars += len(section)
	}

	result := strings.TrimSpace(sb.String())
	if result == "" {
		return fmt.Sprintf("No matches for pattern %q in the last %d days.", pattern, maxDays), nil
	}
	return result, nil
}

// upsertSection inserts or replaces a named section in markdown content.
// If the section header exists, its content is replaced. Otherwise, the section is appended.
// This is a pure function suitable for unit testing.
func upsertSection(existing, sectionHeader, content string) string {
	if existing == "" {
		return fmt.Sprintf("# Memory\n\n%s\n\n%s\n", sectionHeader, content)
	}

	lines := strings.Split(existing, "\n")
	sectionIdx := -1

	// Find the section header line
	for i, line := range lines {
		if line == sectionHeader {
			sectionIdx = i
			break
		}
	}

	if sectionIdx == -1 {
		// Section doesn't exist, append at end
		trimmed := strings.TrimRight(existing, "\n")
		return fmt.Sprintf("%s\n\n%s\n\n%s\n", trimmed, sectionHeader, content)
	}

	// Find the end of this section (next ## header or end of file)
	endIdx := len(lines)
	for i := sectionIdx + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			endIdx = i
			break
		}
	}

	// Build the new content
	before := strings.Join(lines[:sectionIdx], "\n")
	after := ""
	if endIdx < len(lines) {
		after = strings.Join(lines[endIdx:], "\n")
	}

	// Remove trailing newlines from before part for clean formatting
	before = strings.TrimRight(before, "\n")

	if after == "" {
		// Section is at end of file
		return fmt.Sprintf("%s\n\n%s\n\n%s\n", before, sectionHeader, content)
	}

	// Add a single newline before the next section
	return fmt.Sprintf("%s\n\n%s\n\n%s\n\n%s", before, sectionHeader, content, after)
}
