package agentcontext

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed system_prompt.md
var embeddedSystemPrompt string

// SystemPrompt returns the canonical base system prompt for context composition.
func SystemPrompt() string {
	return embeddedSystemPrompt
}

// Builder creates context snapshots for a workspace.
type Builder struct {
	basePrompt string
}

// NewBuilder creates a Builder. If basePrompt is empty, a default is used.
func NewBuilder(basePrompt string) *Builder {
	if strings.TrimSpace(basePrompt) == "" {
		basePrompt = embeddedSystemPrompt
	}
	return &Builder{basePrompt: strings.TrimSpace(basePrompt)}
}

// Build assembles the immutable context snapshot for a workspace.
func (b *Builder) Build(workspaceDir string) (Snapshot, error) {
	identity, err := LoadIdentity(workspaceDir)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load identity: %w", err)
	}
	skills, err := LoadSkills(workspaceDir)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load skills: %w", err)
	}

	prompt := b.composeSystemPrompt(identity, skills)
	return Snapshot{
		SystemPrompt: prompt,
		Identity:     identity,
		Skills:       skills,
	}, nil
}

func (b *Builder) composeSystemPrompt(identity string, skills []Skill) string {
	var sb strings.Builder
	sb.WriteString(strings.TrimSpace(b.basePrompt))

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

// BasePrompt returns the builder's resolved base prompt.
func (b *Builder) BasePrompt() string {
	return b.basePrompt
}

// BuildRunContext applies compaction policy and returns LLM-ready context.
func (b *Builder) BuildRunContext(messages []Message) RunContext {
	return RunContext{
		Messages: append([]Message{}, messages...),
	}
}
