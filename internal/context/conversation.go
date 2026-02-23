package agentcontext

// Context owns the in-memory conversation state for one session.
type Context struct {
	systemPrompt string
	messages     []Message
}

// NewContext builds a context state with a base system prompt.
func NewContext(systemPrompt string, initialMessages []Message) Context {
	return Context{
		systemPrompt: systemPrompt,
		messages:     CloneMessages(initialMessages),
	}
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
