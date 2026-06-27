package agentcontext

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"valinor/internal/logger"
)

const IdentityFile = "IDENTITY.md"
const SkillsDir = "skills"

const MemoryFile = "MEMORY.md"
const MaxMemoryMainChars = 15000

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
		logger.Error("context load identity read failed", logger.BuildMetadata("path", path, "error", err))
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
	logger.Debug("context build start", logger.BuildMetadata("workspace_dir", workspaceDir))
	identity, err := LoadIdentity(workspaceDir)
	if err != nil {
		logger.Error("context build identity failed", logger.BuildMetadata("workspace_dir", workspaceDir, "error", err))
		return "", fmt.Errorf("load identity: %w", err)
	}
	skills, err := LoadSkills(workspaceDir)
	if err != nil {
		logger.Error("context build skills failed", logger.BuildMetadata("workspace_dir", workspaceDir, "error", err))
		return "", fmt.Errorf("load skills: %w", err)
	}
	memoryContent, err := LoadMemory(workspaceDir)
	if err != nil {
		logger.Error("context build memory failed", logger.BuildMetadata("workspace_dir", workspaceDir, "error", err))
		return "", fmt.Errorf("load memory: %w", err)
	}

	prompt := composeSystemPrompt(SystemPrompt(), identity, skills, memoryContent)
	logger.Info("context build success", logger.BuildMetadata("workspace_dir", workspaceDir, "identity_size", len(identity), "skill_count", len(skills), "memory_size", len(memoryContent)))
	return prompt, nil
}

// LoadSkills scans skills/*.md and skills/*/SKILL.md and returns deterministic metadata for each skill.
// Missing skills directory is treated as empty catalog.
func LoadSkills(workspaceDir string) ([]Skill, error) {
	dir := filepath.Join(workspaceDir, SkillsDir)
	entries, err := os.ReadDir(dir)
	logger.Debug("context load skills dir read", logger.BuildMetadata("dir", dir, "entries", entries))
	if err != nil {
		// Missing skills directory is treated as empty catalog.
		if os.IsNotExist(err) {
			return nil, nil
		}
		logger.Error("context load skills dir read failed", logger.BuildMetadata("dir", dir, "error", err))
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
			logger.Error("context load skills file read failed", logger.BuildMetadata("file", name, "error", err))
			return nil, fmt.Errorf("read skill file %q: %w", name, err)
		}

		skillName, description, ok := parseSkillHeader(string(data))
		if !ok {
			continue
		}
		logger.Debug("context load skills file parsed", logger.BuildMetadata("file", name, "skill_name", skillName, "description", description))
		out = append(out, Skill{
			Name:        skillName,
			Description: description,
			Path:        filepath.ToSlash(filepath.Join(SkillsDir, name)),
		})
	}
	return out, nil
}

// LoadSkillBodies reads full skill file contents for the given skill names.
// Unknown skill names are skipped. Returns empty string when no skills match.
func LoadSkillBodies(workspaceDir string, skillNames []string) (string, error) {
	if len(skillNames) == 0 {
		return "", nil
	}

	catalog, err := LoadSkills(workspaceDir)
	if err != nil {
		return "", err
	}

	byName := make(map[string]Skill, len(catalog))
	for _, skill := range catalog {
		byName[skill.Name] = skill
	}

	var sb strings.Builder
	for _, name := range skillNames {
		skill, ok := byName[name]
		if !ok {
			continue
		}
		data, err := os.ReadFile(filepath.Join(workspaceDir, skill.Path))
		if err != nil {
			return "", fmt.Errorf("read skill %q: %w", name, err)
		}
		sb.WriteString("## Skill: ")
		sb.WriteString(name)
		sb.WriteString("\n\n")
		sb.WriteString(string(data))
		sb.WriteString("\n\n")
	}
	return strings.TrimSpace(sb.String()), nil
}

// LoadMemory reads MEMORY.md from the workspace for injection into the system prompt.
// Daily logs are not included; use memory_search to query older daily entries.
// Missing files are silently ignored (empty string returned).
func LoadMemory(workspaceDir string) (string, error) {
	mainPath := filepath.Join(workspaceDir, MemoryFile)
	mainData, err := os.ReadFile(mainPath)
	if err != nil && !os.IsNotExist(err) {
		logger.Error("context load memory read failed", logger.BuildMetadata("path", mainPath, "error", err))
		return "", fmt.Errorf("read memory file: %w", err)
	}
	mainContent := strings.TrimSpace(string(mainData))
	if mainContent == "" {
		return "", nil
	}
	if len(mainContent) > MaxMemoryMainChars {
		mainContent = mainContent[:MaxMemoryMainChars] + "\n\n... (truncated)"
	}
	return mainContent, nil
}

// loadNestedSkill attempts to load a SKILL.md file from a nested directory.
// Returns nil if no valid skill is found.
func loadNestedSkill(skillsDir, subdir string) *Skill {
	skillPath := filepath.Join(skillsDir, subdir, "SKILL.md")
	logger.Debug("context load nested skill checking", logger.BuildMetadata("dir", subdir, "path", skillPath))
	data, err := os.ReadFile(skillPath)
	if err != nil {
		// No SKILL.md in this directory is not an error - just log and skip
		logger.Debug("context load nested skill no SKILL.md found", logger.BuildMetadata("dir", subdir, "path", skillPath, "error", err))
		return nil
	}

	skillName, description, ok := parseSkillHeader(string(data))
	if !ok {
		logger.Debug("context load nested skill invalid header", logger.BuildMetadata("dir", subdir))
		return nil
	}

	logger.Debug("context load nested skill parsed", logger.BuildMetadata("dir", subdir, "skill_name", skillName, "description", description))
	return &Skill{
		Name:        skillName,
		Description: description,
		Path:        filepath.ToSlash(filepath.Join(SkillsDir, subdir, "SKILL.md")),
	}
}

func composeSystemPrompt(basePrompt, identity string, skills []Skill, memory string) string {
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(basePrompt))

	sb.WriteString("\n\n")
	sb.WriteString(currentDateContext())

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

	sb.WriteString("\n## LoadedMemory\n")
	sb.WriteString("MEMORY.md snapshot from session start (updates via memory_update take effect on the next session). Use memory_search for daily logs.\n\n")
	if strings.TrimSpace(memory) == "" {
		sb.WriteString("MEMORY.md is empty. Use memory_update to add durable sections.\n")
	} else {
		sb.WriteString(memory)
		sb.WriteString("\n")
	}

	return strings.TrimSpace(sb.String())
}

func currentDateContext() string {
	now := time.Now()
	return fmt.Sprintf(
		"## Current Date\n\nToday is %s (%s). Use this date for \"today\", \"now\", and other time-sensitive requests. Do not guess the year.",
		now.Format("Monday, January 2, 2006"),
		now.Format("2006-01-02"),
	)
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
