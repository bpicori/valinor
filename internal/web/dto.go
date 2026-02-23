package web

import (
	"fmt"
	"time"

	"valinor/internal/bus"
	agentcontext "valinor/internal/context"
	"valinor/internal/session"
)

// SessionDTO is an API-safe session representation.
type SessionDTO struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Provider  string    `json:"provider,omitempty"`
	Model     string    `json:"model,omitempty"`
}

// ToolCallDTO represents one assistant tool call in the timeline.
type ToolCallDTO struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Args   string `json:"args"`
	Result string `json:"result,omitempty"`
	Done   bool   `json:"done"`
}

// TimelineMessageDTO is one rendered chat message.
type TimelineMessageDTO struct {
	Role      string        `json:"role"`
	Content   string        `json:"content,omitempty"`
	ToolCalls []ToolCallDTO `json:"tool_calls,omitempty"`
	Streaming bool          `json:"streaming,omitempty"`
	Error     string        `json:"error,omitempty"`
}

// BusEventDTO is sent over websocket to web clients.
type BusEventDTO struct {
	Type    string `json:"type"`
	Payload any    `json:"payload,omitempty"`
}

type wsUserMessagePayload struct {
	Content string `json:"content"`
}

type wsContentPayload struct {
	Content string `json:"content"`
}

type wsToolCallStartPayload struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	ToolArgs   string `json:"tool_args"`
}

type wsToolCallDonePayload struct {
	ToolCallID string `json:"tool_call_id"`
	ToolName   string `json:"tool_name"`
	ToolResult string `json:"tool_result"`
}

type wsAgentDonePayload struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

type postMessageRequest struct {
	Content string `json:"content"`
}

type modelInfoDTO struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

type patchSessionModelRequest struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func toSessionDTOs(items []session.Metadata) []SessionDTO {
	out := make([]SessionDTO, 0, len(items))
	for _, item := range items {
		out = append(out, SessionDTO{
			ID:        item.ID,
			Title:     item.Title,
			CreatedAt: item.CreatedAt,
			UpdatedAt: item.UpdatedAt,
			Provider:  item.Provider,
			Model:     item.Model,
		})
	}
	return out
}

func toBusEventDTO(ev bus.Event) (BusEventDTO, error) {
	switch ev.Type {
	case bus.EventUserMessage:
		p, ok := ev.Payload.(bus.UserMessagePayload)
		if !ok {
			return BusEventDTO{}, fmt.Errorf("invalid user_message payload")
		}
		return BusEventDTO{
			Type:    "user_message",
			Payload: wsUserMessagePayload{Content: p.Content},
		}, nil
	case bus.EventNewConversation:
		return BusEventDTO{Type: "new_conversation"}, nil
	case bus.EventChangeProviderModel:
		p, ok := ev.Payload.(bus.ChangeProviderModelPayload)
		if !ok {
			return BusEventDTO{}, fmt.Errorf("invalid change_provider_model payload")
		}
		return BusEventDTO{
			Type: "change_provider_model",
			Payload: map[string]string{
				"provider": p.Provider,
				"model":    p.Model,
			},
		}, nil
	case bus.EventAgentContentDelta:
		p, ok := ev.Payload.(bus.ContentDeltaPayload)
		if !ok {
			return BusEventDTO{}, fmt.Errorf("invalid agent_content_delta payload")
		}
		return BusEventDTO{
			Type:    "agent_content_delta",
			Payload: wsContentPayload{Content: p.Content},
		}, nil
	case bus.EventAgentRefusal:
		p, ok := ev.Payload.(bus.RefusalPayload)
		if !ok {
			return BusEventDTO{}, fmt.Errorf("invalid agent_refusal payload")
		}
		return BusEventDTO{
			Type:    "agent_refusal",
			Payload: wsContentPayload{Content: p.Content},
		}, nil
	case bus.EventAgentToolCallStart:
		p, ok := ev.Payload.(bus.ToolCallStartPayload)
		if !ok {
			return BusEventDTO{}, fmt.Errorf("invalid agent_tool_call_start payload")
		}
		return BusEventDTO{
			Type: "agent_tool_call_start",
			Payload: wsToolCallStartPayload{
				ToolCallID: p.ToolCallID,
				ToolName:   p.ToolName,
				ToolArgs:   p.ToolArgs,
			},
		}, nil
	case bus.EventAgentToolCallDone:
		p, ok := ev.Payload.(bus.ToolCallDonePayload)
		if !ok {
			return BusEventDTO{}, fmt.Errorf("invalid agent_tool_call_done payload")
		}
		return BusEventDTO{
			Type: "agent_tool_call_done",
			Payload: wsToolCallDonePayload{
				ToolCallID: p.ToolCallID,
				ToolName:   p.ToolName,
				ToolResult: p.ToolResult,
			},
		}, nil
	case bus.EventAgentDone:
		p, ok := ev.Payload.(bus.AgentDonePayload)
		if !ok {
			return BusEventDTO{}, fmt.Errorf("invalid agent_done payload")
		}
		return BusEventDTO{
			Type: "agent_done",
			Payload: wsAgentDonePayload{
				PromptTokens:     p.PromptTokens,
				CompletionTokens: p.CompletionTokens,
				TotalTokens:      p.TotalTokens,
			},
		}, nil
	case bus.EventAgentError:
		p, ok := ev.Payload.(bus.AgentErrorPayload)
		if !ok {
			return BusEventDTO{}, fmt.Errorf("invalid agent_error payload")
		}
		msg := "unknown error"
		if p.Error != nil {
			msg = p.Error.Error()
		}
		return BusEventDTO{
			Type: "agent_error",
			Payload: map[string]string{
				"error": msg,
			},
		}, nil
	default:
		return BusEventDTO{}, fmt.Errorf("unsupported event type: %d", ev.Type)
	}
}

func toTimelineMessages(storedMessages []agentcontext.Message) []TimelineMessageDTO {
	messages := make([]TimelineMessageDTO, 0)
	for _, msg := range storedMessages {
		switch msg.Role {
		case agentcontext.RoleSystem:
			// System prompt is not part of the timeline UI.
		case agentcontext.RoleUser:
			messages = append(messages, TimelineMessageDTO{
				Role:    "user",
				Content: msg.Content,
			})
		case agentcontext.RoleAssistant:
			assistant := ensureAssistantMessage(&messages)
			assistant.Content = msg.Content
		case agentcontext.RoleAssistantToolCalls:
			assistant := ensureAssistantMessage(&messages)
			for _, tc := range msg.ToolCalls {
				assistant.ToolCalls = append(assistant.ToolCalls, ToolCallDTO{
					ID:   tc.ID,
					Name: tc.Name,
					Args: tc.Arguments,
				})
			}
		case agentcontext.RoleTool:
			assistant := ensureAssistantMessage(&messages)
			for i := range assistant.ToolCalls {
				if assistant.ToolCalls[i].ID == msg.ToolCallID && !assistant.ToolCalls[i].Done {
					assistant.ToolCalls[i].Result = msg.Content
					assistant.ToolCalls[i].Done = true
					break
				}
			}
		}
	}
	return messages
}

func ensureAssistantMessage(messages *[]TimelineMessageDTO) *TimelineMessageDTO {
	if len(*messages) == 0 || (*messages)[len(*messages)-1].Role != "assistant" {
		*messages = append(*messages, TimelineMessageDTO{Role: "assistant"})
	}
	return &(*messages)[len(*messages)-1]
}
