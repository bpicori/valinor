package agentcontext

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"valinor/internal/applog"
)

const IdentityFile = "IDENTITY.md"
const SkillsDir = "skills"

var (
	skillHeaderRE      = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---`)
	skillNameRE        = regexp.MustCompile(`(?m)^name:\s*(.+)$`)
	skillDescriptionRE = regexp.MustCompile(`(?m)^description:\s*(.+)$`)
	//go:embed system_prompt.md
	embeddedSystemPrompt string
)

// Skill describes a discoverable skill file available to the agent.
// The model gets this metadata in context and can read the file on demand.
type Skill struct {
	Name        string
	Description string
	Path        string
}

// MessageRole is a provider-agnostic conversation role.
type MessageRole string

const (
	RoleSystem             MessageRole = "system"
	RoleUser               MessageRole = "user"
	RoleAssistant          MessageRole = "assistant"
	RoleAssistantToolCalls MessageRole = "assistant_tool_calls"
	RoleTool               MessageRole = "tool"
)

// ToolCall is a provider-agnostic function/tool call.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Message is a provider-agnostic conversation message.
type Message struct {
	Role       MessageRole `json:"role"`
	Content    string      `json:"content,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall  `json:"tool_calls,omitempty"`
}

// CloneMessages returns a shallow copy of a message slice.
func CloneMessages(messages []Message) []Message {
	return append([]Message{}, messages...)
}

// LoadIdentity reads IDENTITY.md from workspace root.
func LoadIdentity(workspaceDir string) (string, error) {
	path := filepath.Join(workspaceDir, IdentityFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		applog.Error("context load identity read failed", applog.BuildMetadata("path", path, "error", err))
		return "", fmt.Errorf("read identity file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

// SystemPrompt returns the canonical base system prompt for context composition.
func SystemPrompt() string {
	return embeddedSystemPrompt
}

// BuildSystemPrompt builds the composed system prompt for the workspace.
func BuildSystemPrompt(workspaceDir string) (string, error) {
	applog.Debug("context build start", applog.BuildMetadata("workspace_dir", workspaceDir))
	identity, err := LoadIdentity(workspaceDir)
	if err != nil {
		applog.Error("context build identity failed", applog.BuildMetadata("workspace_dir", workspaceDir, "error", err))
		return "", fmt.Errorf("load identity: %w", err)
	}
	skills, err := LoadSkills(workspaceDir)
	if err != nil {
		applog.Error("context build skills failed", applog.BuildMetadata("workspace_dir", workspaceDir, "error", err))
		return "", fmt.Errorf("load skills: %w", err)
	}

	prompt := composeSystemPrompt(SystemPrompt(), identity, skills)
	applog.Info("context build success", applog.BuildMetadata("workspace_dir", workspaceDir, "identity_size", len(identity), "skill_count", len(skills)))
	return prompt, nil
}

// LoadSkills scans skills/*.md and skills/*/SKILL.md and returns deterministic metadata for each skill.
// Missing skills directory is treated as empty catalog.
func LoadSkills(workspaceDir string) ([]Skill, error) {
	dir := filepath.Join(workspaceDir, SkillsDir)
	entries, err := os.ReadDir(dir)
	applog.Debug("context load skills dir read", applog.BuildMetadata("dir", dir, "entries", entries))
	if err != nil {
		// Missing skills directory is treated as empty catalog.
		if os.IsNotExist(err) {
			return nil, nil
		}
		applog.Error("context load skills dir read failed", applog.BuildMetadata("dir", dir, "error", err))
		return nil, fmt.Errorf("read skills dir: %w", err)
	}

	out := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			// Look for SKILL.md inside nested directories (e.g., skills/weather/SKILL.md)
			skill := loadNestedSkill(dir, entry.Name())
			if skill != nil {
				out = append(out, *skill)
			}
			continue
		}
		name := entry.Name()
		// Only .md files are considered skills.
		if !strings.HasSuffix(strings.ToLower(name), ".md") {
			continue
		}

		absPath := filepath.Join(dir, name)
		data, err := os.ReadFile(absPath)
		if err != nil {
			applog.Error("context load skills file read failed", applog.BuildMetadata("file", name, "error", err))
			return nil, fmt.Errorf("read skill file %q: %w", name, err)
		}

		skillName, description, ok := parseSkillHeader(string(data))
		if !ok {
			continue
		}
		applog.Debug("context load skills file parsed", applog.BuildMetadata("file", name, "skill_name", skillName, "description", description))
		out = append(out, Skill{
			Name:        skillName,
			Description: description,
			Path:        filepath.ToSlash(filepath.Join(SkillsDir, name)),
		})
	}
	return out, nil
}

// loadNestedSkill attempts to load a SKILL.md file from a nested directory.
// Returns nil if no valid skill is found.
func loadNestedSkill(skillsDir, subdir string) *Skill {
	skillPath := filepath.Join(skillsDir, subdir, "SKILL.md")
	applog.Debug("context load nested skill checking", applog.BuildMetadata("dir", subdir, "path", skillPath))
	data, err := os.ReadFile(skillPath)
	if err != nil {
		// No SKILL.md in this directory is not an error - just log and skip
		applog.Debug("context load nested skill no SKILL.md found", applog.BuildMetadata("dir", subdir, "path", skillPath, "error", err))
		return nil
	}

	skillName, description, ok := parseSkillHeader(string(data))
	if !ok {
		applog.Debug("context load nested skill invalid header", applog.BuildMetadata("dir", subdir))
		return nil
	}

	applog.Debug("context load nested skill parsed", applog.BuildMetadata("dir", subdir, "skill_name", skillName, "description", description))
	return &Skill{
		Name:        skillName,
		Description: description,
		Path:        filepath.ToSlash(filepath.Join(SkillsDir, subdir, "SKILL.md")),
	}
}

func composeSystemPrompt(basePrompt, identity string, skills []Skill) string {
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(basePrompt))

	sb.WriteString("\n\n## AgentIdentity\n")
	if strings.TrimSpace(identity) == "" {
		sb.WriteString("No identity file provided.\n")
	} else {
		sb.WriteString(identity)
		sb.WriteString("\n")
	}

	sb.WriteString("\n## AvailableSkills\n")
	if len(skills) == 0 {
		sb.WriteString("- none\n")
	} else {
		for _, skill := range skills {
			sb.WriteString("- ")
			sb.WriteString(skill.Name)
			sb.WriteString(": ")
			sb.WriteString(skill.Description)
			sb.WriteString(" (path: ")
			sb.WriteString(skill.Path)
			sb.WriteString(")\n")
		}
	}

	return strings.TrimSpace(sb.String())
}

func parseSkillHeader(contents string) (name, description string, ok bool) {
	trimmed := strings.TrimSpace(contents)
	if trimmed == "" {
		return "", "", false
	}

	fm := skillHeaderRE.FindStringSubmatch(trimmed)
	if len(fm) < 2 {
		return "", "", false
	}
	block := fm[1]

	name = ""
	if m := skillNameRE.FindStringSubmatch(block); len(m) >= 2 {
		name = strings.Trim(strings.TrimSpace(m[1]), "\"'")
	}
	if name == "" {
		return "", "", false
	}

	description = ""
	if m := skillDescriptionRE.FindStringSubmatch(block); len(m) >= 2 {
		description = strings.Trim(strings.TrimSpace(m[1]), "\"'")
	}
	if description == "" {
		return "", "", false
	}

	return name, description, true
}
