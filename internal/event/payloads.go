package event

import (
	"encoding/json"
	"errors"
	"time"

	"valinor/internal/cron"
)

// UserMessagePayload carries the content of a user chat message.
type UserMessagePayload struct {
	Content string `json:"content"`
}

// ChangeProviderModelPayload carries provider and model for session switch.
type ChangeProviderModelPayload struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// CreateNewSessionPayload carries provider and model for creating a new session.
type CreateNewSessionPayload struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Force    bool   `json:"force,omitempty"`
}

// RequestProviderModelsPayload carries provider name to fetch available models for.
type RequestProviderModelsPayload struct {
	Provider string `json:"provider"`
}

// ProviderModel describes a model option from a provider.
type ProviderModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

// ProviderModelsResultPayload carries available models fetched for a provider.
type ProviderModelsResultPayload struct {
	Provider string          `json:"provider"`
	Models   []ProviderModel `json:"models"`
	Error    string          `json:"error,omitempty"`
}

// ContentDeltaPayload carries a streamed content chunk from the agent.
type ContentDeltaPayload struct {
	Content string `json:"content"`
}

// ContentFullPayload carries full assistant content for one response.
type ContentFullPayload struct {
	Content string `json:"content"`
}

// ImagePayload carries one generated image for client-side delivery.
type ImagePayload struct {
	FileName string `json:"file_name"`
	MIMEType string `json:"mime_type"`
	Base64   string `json:"base64"`
}

// FilePayload carries one workspace file for client-side delivery. The client
// reads the file from the workspace; bytes are not serialized into the event.
type FilePayload struct {
	Path     string `json:"path"`
	FileName string `json:"file_name"`
	MIMEType string `json:"mime_type,omitempty"`
	Size     int64  `json:"size,omitempty"`
	Caption  string `json:"caption,omitempty"`
}

// QuestionPayload carries a structured question for an interactive client.
type QuestionPayload struct {
	QuestionID    string   `json:"question_id"`
	Question      string   `json:"question"`
	Options       []string `json:"options,omitempty"`
	AllowFreeText bool     `json:"allow_free_text"`
}

// ThinkingPayload carries a reasoning/thinking update.
type ThinkingPayload struct {
	Step    int    `json:"step"`
	Content string `json:"content"`
}

// RefusalPayload carries a model refusal message.
type RefusalPayload struct {
	Content string `json:"content"`
}

// ToolCallStartPayload carries metadata for an initiated tool call.
type ToolCallStartPayload struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	ToolArgs   string `json:"tool_args"`
}

// ToolCallDonePayload carries the result of a completed tool call.
type ToolCallDonePayload struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	ToolResult string `json:"tool_result"`
}

// AgentDonePayload carries token usage for a completed agent turn.
type AgentDonePayload struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// StepTokenUsagePayload carries per-step token usage.
type StepTokenUsagePayload struct {
	Step             int   `json:"step"`
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// ContextTokensPayload carries current context token information.
type ContextTokensPayload struct {
	PromptTokens int64 `json:"prompt_tokens"`
	Estimated    bool  `json:"estimated"`
}

// ContextCompressedPayload reports successful context summarization.
type ContextCompressedPayload struct {
	MessagesCompressed int   `json:"messages_compressed"`
	TokensBefore       int64 `json:"tokens_before"`
	TokensAfter        int64 `json:"tokens_after"`
}

// ContextCompressionFailedPayload reports a failed context summarization attempt.
type ContextCompressionFailedPayload struct {
	EstimatedTokens int64  `json:"estimated_tokens"`
	ContextLimit    int64  `json:"context_limit"`
	Error           string `json:"error"`
}

// AgentErrorPayload carries an error from the agent.
type AgentErrorPayload struct {
	Error error `json:"-"` // Error is not JSON serializable, use custom marshaling.
}

// MarshalJSON implements custom JSON marshaling for AgentErrorPayload.
func (p AgentErrorPayload) MarshalJSON() ([]byte, error) {
	msg := ""
	if p.Error != nil {
		msg = p.Error.Error()
	}
	return json.Marshal(map[string]string{"error": msg})
}

// UnmarshalJSON implements custom JSON unmarshaling for AgentErrorPayload.
func (p *AgentErrorPayload) UnmarshalJSON(data []byte) error {
	var tmp struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	if tmp.Error != "" {
		p.Error = errors.New(tmp.Error)
	}
	return nil
}

// AgentCancelledPayload indicates the agent run was cancelled by the user.
type AgentCancelledPayload struct{}

// SessionAttachedPayload indicates a session has been attached to this identity.
type SessionAttachedPayload struct {
	SessionKey string `json:"session_key"`
	Provider   string `json:"provider"`
	Model      string `json:"model"`
}

// CronJobResultPayload carries the result of a scheduled cron job execution.
type CronJobResultPayload struct {
	JobID     string              `json:"job_id,omitempty"`   // ID of the job that ran
	JobName   string              `json:"job_name,omitempty"` // Name of the job
	Content   string              `json:"content"`
	Timestamp time.Time           `json:"timestamp"`
	DeliverTo cron.DeliveryTarget `json:"deliver_to,omitempty"`
	CreatedBy cron.ClientRef      `json:"created_by,omitempty"`
}
