package agentcontext

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"valinor/internal/applog"
)

const IdentityFile = "IDENTITY.md"

// Skill describes a discoverable skill file available to the agent.
// The model gets this metadata in context and can read the file on demand.
type Skill struct {
	Name        string
	Description string
	Path        string
}

// Snapshot is the immutable context payload used to initialize and restore agent state.
type Snapshot struct {
	SystemPrompt string
	Identity     string
	Skills       []Skill
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

// RunContext is the built context passed to the LLM.
type RunContext struct {
	Messages []Message
}

// LoadIdentity reads IDENTITY.md from workspace root.
// Missing files are treated as empty identity.
func LoadIdentity(workspaceDir string) (string, error) {
	path := filepath.Join(workspaceDir, IdentityFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		applog.Error("context load identity read failed", applog.M("path", path, "error", err))
		return "", fmt.Errorf("read identity file: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}
