package agentcontext

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"valinor/internal/applog"
)

var (
	frontMatterRE = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---`)
	nameRE        = regexp.MustCompile(`(?m)^name:\s*(.+)$`)
	descriptionRE = regexp.MustCompile(`(?m)^description:\s*(.+)$`)
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
		applog.Error("context load skills dir read failed", applog.M("dir", dir, "error", err))
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
			applog.Error("context load skills file read failed", applog.M("file", name, "error", err))
			return nil, fmt.Errorf("read skill file %q: %w", name, err)
		}

		skillName, description, ok := parseSkillMetadata(name, string(data))
		if !ok {
			continue
		}
		applog.Debug("context load skills file parsed", applog.M("file", name, "skill_name", skillName, "description", description))
		out = append(out, Skill{
			Name:        skillName,
			Description: description,
			Path:        filepath.ToSlash(filepath.Join(skillsDir, name)),
		})
	}
	return out, nil
}

func parseSkillMetadata(fileName, contents string) (name, description string, ok bool) {
	defaultName := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	trimmed := strings.TrimSpace(contents)
	if trimmed == "" {
		return "", "", false
	}

	frontName, frontDesc := parseFrontMatterMetadata(trimmed)
	if frontName != "" || frontDesc != "" {
		if frontName == "" {
			frontName = defaultName
		}
		if frontDesc == "" {
			return "", "", false
		}
		return frontName, frontDesc, true
	}

	name = defaultName
	description = ""
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
				return name, description, true
			}
			continue
		}
		description = line
		return name, description, true
	}
	return "", "", false
}

func parseFrontMatterMetadata(contents string) (string, string) {
	fm := frontMatterRE.FindStringSubmatch(contents)
	if len(fm) < 2 {
		return "", ""
	}
	block := fm[1]

	name := ""
	if m := nameRE.FindStringSubmatch(block); len(m) >= 2 {
		name = strings.Trim(strings.TrimSpace(m[1]), "\"'")
	}

	description := ""
	if m := descriptionRE.FindStringSubmatch(block); len(m) >= 2 {
		description = strings.Trim(strings.TrimSpace(m[1]), "\"'")
	}

	return name, description
}
