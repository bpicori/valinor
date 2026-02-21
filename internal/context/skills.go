package agentcontext

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const skillsDir = "skills"

// LoadSkills scans skills/*.md and returns deterministic metadata for each skill.
// Missing skills directory is treated as empty catalog.
func LoadSkills(workspaceDir string) ([]Skill, error) {
	dir := filepath.Join(workspaceDir, skillsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read skills dir: %w", err)
	}

	out := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}

		absPath := filepath.Join(dir, name)
		data, err := os.ReadFile(absPath)
		if err != nil {
			return nil, fmt.Errorf("read skill file %q: %w", name, err)
		}

		skillName, description := parseSkillMetadata(name, string(data))
		out = append(out, Skill{
			Name:        skillName,
			Description: description,
			Path:        filepath.ToSlash(filepath.Join(skillsDir, name)),
		})
	}
	return out, nil
}

func parseSkillMetadata(fileName, contents string) (string, string) {
	defaultName := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	trimmed := strings.TrimSpace(contents)
	if trimmed == "" {
		return defaultName, "No description provided."
	}

	frontName, frontDesc := parseFrontMatterMetadata(trimmed)
	if frontName != "" || frontDesc != "" {
		if frontName == "" {
			frontName = defaultName
		}
		if frontDesc == "" {
			frontDesc = "No description provided."
		}
		return frontName, frontDesc
	}

	name := defaultName
	description := "No description provided."
	scanner := bufio.NewScanner(strings.NewReader(trimmed))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			title := strings.TrimSpace(strings.TrimLeft(line, "#"))
			if title != "" {
				name = title
			}
			continue
		}
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "description:") {
			desc := strings.TrimSpace(line[len("description:"):])
			if desc != "" {
				description = desc
				break
			}
			continue
		}
		description = line
		break
	}
	return name, description
}

func parseFrontMatterMetadata(contents string) (string, string) {
	lines := strings.Split(contents, "\n")
	if len(lines) < 3 {
		return "", ""
	}
	if strings.TrimSpace(lines[0]) != "---" {
		return "", ""
	}

	name := ""
	description := ""
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if line == "---" {
			break
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(parts[0]))
		value := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
		switch key {
		case "name":
			name = value
		case "description":
			description = value
		}
	}
	return name, description
}
