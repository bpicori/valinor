// Package telegram provides command parsing and update dispatching.
package telegram

import (
	"fmt"
	"strings"

	"valinor/internal/gateway"
)

// Command represents a parsed Telegram command ready for routing.
type Command struct {
	// Identity is the chat identity for session routing
	Identity ChatIdentity
	// RequestID is a correlation ID for tracking
	RequestID string
	// Type is the router command type
	Type string
	// Payload is the typed command payload
	Payload interface{}
	// RawText is the original message text
	RawText string
	// IsControlCommand is true if this is a cancel/stop command
	IsControlCommand bool
}


// dispatch parses an update and returns a command if it should be processed.
// Returns nil if the update should be ignored (e.g., no message, etc.).
func (b *Bot) dispatch(update Update) (*Command, error) {
	// Extract message from update
	msg := b.extractMessage(update)
	if msg == nil {
		return nil, nil // No message to process
	}

	// Build chat identity
	identity := FromTelegramUpdate(msg.Chat)

	// Parse command or message
	return b.parseCommand(identity, msg, update.UpdateID)
}

// extractMessage extracts the message from an update.
func (b *Bot) extractMessage(update Update) *Message {
	if update.Message != nil {
		return update.Message
	}
	if update.EditedMessage != nil {
		return update.EditedMessage
	}
	// Callback queries don't have direct messages, handled separately
	return nil
}

// parseCommand parses a message into a Command.
func (b *Bot) parseCommand(identity ChatIdentity, msg *Message, updateID int64) (*Command, error) {
	requestID := fmt.Sprintf("tg_%d_%d", msg.Chat.ID, updateID)

	// Check for commands
	if msg.IsCommand() {
		cmd, args := msg.CommandWithArgs()
		cmdLower := strings.ToLower(cmd)

		switch cmdLower {
		case "new", "clear":
			return &Command{
				Identity:  identity,
				RequestID: requestID,
				Type:      gateway.CommandNewConversation,
				RawText:   msg.Text,
			}, nil

		case "cancel":
			return &Command{
				Identity:         identity,
				RequestID:        requestID,
				Type:             gateway.CommandCancelAgentRun,
				RawText:          msg.Text,
				IsControlCommand: true,
			}, nil

		case "model":
			// /model provider model - requires args
			parts := strings.Fields(args)
			if len(parts) >= 2 {
				return &Command{
					Identity:  identity,
					RequestID: requestID,
					Type:      gateway.CommandChangeProviderModel,
					Payload: gateway.ChangeProviderModelPayload{
						Provider: parts[0],
						Model:    strings.Join(parts[1:], " "),
					},
					RawText: msg.Text,
				}, nil
			}
			// Invalid model command - treat as regular message explaining usage
			return &Command{
				Identity:  identity,
				RequestID: requestID,
				Type:      gateway.CommandUserMessage,
				Payload: gateway.UserMessagePayload{
					Content: "To change the model, use: /model <provider> <model>",
				},
				RawText: msg.Text,
			}, nil

		case "status":
			return &Command{
				Identity:  identity,
				RequestID: requestID,
				Type:      "status", // Internal command type, not sent to router
				RawText:   msg.Text,
			}, nil

		default:
			// Unknown command - treat as regular message
			return &Command{
				Identity:  identity,
				RequestID: requestID,
				Type:      gateway.CommandUserMessage,
				Payload: gateway.UserMessagePayload{
					Content: msg.Text,
				},
				RawText: msg.Text,
			}, nil
		}
	}

	// Regular message
	return &Command{
		Identity:  identity,
		RequestID: requestID,
		Type:      gateway.CommandUserMessage,
		Payload: gateway.UserMessagePayload{
			Content: msg.Text,
		},
		RawText: msg.Text,
	}, nil
}
