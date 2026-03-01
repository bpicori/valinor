package agentcontext

import "valinor/internal/tools"

// Context owns the in-memory conversation state and tools for one session.
type Context struct {
	systemPrompt string
	messages     []Message
	tools        []tools.Tool
}

// NewContext builds a context state with a base system prompt and tools.
func NewContext(systemPrompt string, initialMessages []Message, tools []tools.Tool) Context {
	return Context{
		systemPrompt: systemPrompt,
		messages:     CloneMessages(initialMessages),
		tools:        tools,
	}
}

// Tools returns the tools available in this context.
func (c *Context) Tools() []tools.Tool {
	return c.tools
}

// SetTools updates the tools available in this context.
func (c *Context) SetTools(tools []tools.Tool) {
	c.tools = tools
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
