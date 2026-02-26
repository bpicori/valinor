// Package telegram provides update parsing and command dispatching.
package telegram

import (
	"context"
	"fmt"
	"strings"

	"valinor/internal/applog"
	"valinor/internal/router"
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

// Dispatcher parses Telegram updates and converts them to router commands.
type Dispatcher struct {
	// RequireMention in groups requires the bot to be mentioned
	RequireMention bool
	// BotUsername is the bot's username for mention checking
	BotUsername string
}

// NewDispatcher creates a new update dispatcher.
func NewDispatcher(botUsername string) *Dispatcher {
	return &Dispatcher{
		BotUsername:    botUsername,
		RequireMention: false,
	}
}

// SetRequireMention configures whether the bot requires being mentioned in groups.
func (d *Dispatcher) SetRequireMention(require bool) {
	d.RequireMention = require
}

// Dispatch parses an update and returns a command if it should be processed.
// Returns nil if the update should be ignored (e.g., not allowed, no message, etc.).
func (d *Dispatcher) Dispatch(ctx context.Context, update Update) (*Command, error) {
	// Extract message from update
	msg := d.extractMessage(update)
	if msg == nil {
		return nil, nil // No message to process
	}

	// Check mention requirement in groups
	if d.RequireMention && d.isGroupChat(msg.Chat) && !d.isMentioned(msg) {
		applog.Debug("telegram dispatcher: bot not mentioned in group",
			applog.BuildMetadata("chat_id", msg.Chat.ID))
		return nil, nil
	}

	// Build chat identity
	identity := FromTelegramUpdate(msg.Chat)
	if msg.MessageThreadID != nil {
		identity = identity.WithThreadID(*msg.MessageThreadID)
	}

	// Parse command or message
	return d.parseCommand(identity, msg, update.UpdateID)
}

// extractMessage extracts the message from an update.
func (d *Dispatcher) extractMessage(update Update) *Message {
	if update.Message != nil {
		return update.Message
	}
	if update.EditedMessage != nil {
		return update.EditedMessage
	}
	if update.ChannelPost != nil {
		return update.ChannelPost
	}
	// Callback queries don't have direct messages, handled separately
	return nil
}

// isGroupChat returns true if the chat is a group or supergroup.
func (d *Dispatcher) isGroupChat(chat *Chat) bool {
	if chat == nil {
		return false
	}
	return chat.Type == "group" || chat.Type == "supergroup"
}

// isMentioned checks if the bot is mentioned in the message.
func (d *Dispatcher) isMentioned(msg *Message) bool {
	if msg == nil || d.BotUsername == "" {
		return false
	}

	botUsernameLower := strings.ToLower(d.BotUsername)

	for _, entity := range msg.Entities {
		if entity.Type == "mention" {
			// Extract the mention text
			start := entity.Offset
			end := entity.Offset + entity.Length
			if start >= 0 && end <= int64(len(msg.Text)) {
				mention := strings.ToLower(msg.Text[start:end])
				if mention == "@"+botUsernameLower {
					return true
				}
			}
		}
		if entity.Type == "text_mention" {
			// Text mention with user object
			if entity.User != nil && strings.ToLower(entity.User.Username) == botUsernameLower {
				return true
			}
		}
	}

	// Also check if message text starts with @botname
	if strings.HasPrefix(strings.ToLower(msg.Text), "@"+botUsernameLower+" ") ||
		strings.ToLower(msg.Text) == "@"+botUsernameLower {
		return true
	}

	return false
}

// parseCommand parses a message into a Command.
func (d *Dispatcher) parseCommand(identity ChatIdentity, msg *Message, updateID int64) (*Command, error) {
	requestID := fmt.Sprintf("tg_%d_%d", msg.Chat.ID, updateID)

	// Check for commands
	if msg.IsCommand() {
		cmd, args := msg.CommandWithArgs()
		cmdLower := strings.ToLower(cmd)

		switch cmdLower {
		case "new", "start":
			return &Command{
				Identity:  identity,
				RequestID: requestID,
				Type:      router.CommandNewConversation,
				RawText:   msg.Text,
			}, nil

		case "cancel", "stop":
			return &Command{
				Identity:         identity,
				RequestID:        requestID,
				Type:             router.CommandCancelAgentRun,
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
					Type:      router.CommandChangeProviderModel,
					Payload: router.ChangeProviderModelPayload{
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
				Type:      router.CommandUserMessage,
				Payload: router.UserMessagePayload{
					Content: "To change the model, use: /model <provider> <model>",
				},
				RawText: msg.Text,
			}, nil

		case "session":
			// /session provider model - requires args
			parts := strings.Fields(args)
			if len(parts) >= 2 {
				return &Command{
					Identity:  identity,
					RequestID: requestID,
					Type:      router.CommandCreateNewSession,
					Payload: router.CreateNewSessionPayload{
						Provider: parts[0],
						Model:    strings.Join(parts[1:], " "),
					},
					RawText: msg.Text,
				}, nil
			}
			// Invalid session command - treat as regular message explaining usage
			return &Command{
				Identity:  identity,
				RequestID: requestID,
				Type:      router.CommandUserMessage,
				Payload: router.UserMessagePayload{
					Content: "To create a new session, use: /session <provider> <model>",
				},
				RawText: msg.Text,
			}, nil

		default:
			// Unknown command - treat as regular message
			return &Command{
				Identity:  identity,
				RequestID: requestID,
				Type:      router.CommandUserMessage,
				Payload: router.UserMessagePayload{
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
		Type:      router.CommandUserMessage,
		Payload: router.UserMessagePayload{
			Content: msg.Text,
		},
		RawText: msg.Text,
	}, nil
}

// GetSequentialKey returns the appropriate sequential key for the command.
// Control commands use a separate lane to avoid blocking regular messages.
func (c *Command) GetSequentialKey() string {
	if c.IsControlCommand {
		return c.Identity.ControlLaneKey()
	}
	return c.Identity.SequentialKey()
}
