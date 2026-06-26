package agentcontext

import (
	"fmt"

	"valinor/internal/tools"
)

// Context owns the in-memory conversation state and tools for one session.
type Context struct {
	systemPrompt string
	messages     []Message
	tools        []tools.Tool
	runContext   *tools.RunContext
}

// New creates a fresh context with a built system prompt and tools.
func New(workspaceDir string, providerName, model string) (Context, error) {
	systemPrompt, err := BuildSystemPrompt(workspaceDir)
	if err != nil {
		return Context{}, fmt.Errorf("build system prompt: %w", err)
	}
	builtTools, runContext, err := tools.BuildTools(workspaceDir, providerName, model)
	if err != nil {
		return Context{}, fmt.Errorf("build tools: %w", err)
	}
	return Context{
		systemPrompt: systemPrompt,
		messages:     []Message{{Role: RoleSystem, Content: systemPrompt}},
		tools:        builtTools,
		runContext:   runContext,
	}, nil
}

// Resume creates a context from persisted messages, normalizing the system prompt.
func Resume(workspaceDir string, providerName, model string, messages []Message) (Context, error) {
	systemPrompt, err := BuildSystemPrompt(workspaceDir)
	if err != nil {
		return Context{}, fmt.Errorf("build system prompt: %w", err)
	}
	builtTools, runContext, err := tools.BuildTools(workspaceDir, providerName, model)
	if err != nil {
		return Context{}, fmt.Errorf("build tools: %w", err)
	}
	return Context{
		systemPrompt: systemPrompt,
		messages:     normalizeSystemPrompt(CloneMessages(messages), systemPrompt),
		tools:        builtTools,
		runContext:   runContext,
	}, nil
}

// normalizeSystemPrompt ensures the first message is the system prompt.
func normalizeSystemPrompt(messages []Message, systemPrompt string) []Message {
	if len(messages) == 0 {
		return []Message{{Role: RoleSystem, Content: systemPrompt}}
	}
	if messages[0].Role == RoleSystem {
		messages[0].Content = systemPrompt
		return messages
	}
	return append([]Message{{Role: RoleSystem, Content: systemPrompt}}, messages...)
}

// Tools returns the tools available in this context.
func (c *Context) Tools() []tools.Tool {
	return c.tools
}

// RunContext returns shared per-run metadata for context-bound tools.
func (c *Context) RunContext() *tools.RunContext {
	return c.runContext
}

// SetTools updates the tools available in this context.
func (c *Context) SetTools(tools []tools.Tool, runContext *tools.RunContext) {
	c.tools = tools
	c.runContext = runContext
}

// AppendUserMessage appends a user message to the context.
func (c *Context) AppendUserMessage(content string) {
	c.messages = append(c.messages, Message{
		Role:    RoleUser,
		Content: content,
	})
}

// ReplaceMessages replaces the full context message history.
func (c *Context) ReplaceMessages(messages []Message) {
	c.messages = CloneMessages(messages)
}

// Reset keeps only the base system message.
func (c *Context) Reset() {
	c.messages = []Message{
		{
			Role:    RoleSystem,
			Content: c.systemPrompt,
		},
	}
}

// Messages returns a cloned copy of the current context messages.
func (c *Context) Messages() []Message {
	return CloneMessages(c.messages)
}

// RunData holds the snapshot needed for an LLM run.
type RunData struct {
	Messages   []Message
	Tools      []tools.Tool
	RunContext *tools.RunContext
}

// PrepareRun appends the user message and returns a snapshot of messages and tools for the LLM.
func (c *Context) PrepareRun(userMessage string) RunData {
	c.messages = append(c.messages, Message{Role: RoleUser, Content: userMessage})
	return RunData{
		Messages:   CloneMessages(c.messages),
		Tools:      c.tools,
		RunContext: c.runContext,
	}
}

// CompleteRun replaces internal messages with the agent's result and returns a clone for persistence.
func (c *Context) CompleteRun(messages []Message) []Message {
	c.messages = CloneMessages(messages)
	return CloneMessages(c.messages)
}