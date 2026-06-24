// Package telegram provides command parsing and update dispatching.
package telegram

import (
	"fmt"
	"strings"

	"valinor/internal/config"
	"valinor/internal/gateway"
)

// Command represents a parsed Telegram command ready for routing.
type Command struct {
	Identity         ChatIdentity
	RequestID        string
	Type             string
	Payload          interface{}
	RawText          string
	IsControlCommand bool
}

// parseUpdate parses a Telegram update into a Command.
// Returns nil if the update should be ignored (e.g., no message, etc.).
func parseUpdate(update Update, providers map[string]config.ProviderConfig) (*Command, error) {
	if update.CallbackQuery != nil {
		return nil, nil
	}

	msg := extractMessage(update)
	if msg == nil {
		return nil, nil
	}

	identity := FromTelegramUpdate(msg.Chat)
	return parseCommand(identity, msg, update.UpdateID, providers)
}

// extractMessage extracts the message from an update.
func extractMessage(update Update) *Message {
	if update.Message != nil {
		return update.Message
	}
	if update.EditedMessage != nil {
		return update.EditedMessage
	}
	return nil
}

// parseCommand parses a message into a Command.
func parseCommand(identity ChatIdentity, msg *Message, updateID int64, providers map[string]config.ProviderConfig) (*Command, error) {
	requestID := fmt.Sprintf("tg_%d_%d", msg.Chat.ID, updateID)

	if msg.IsCommand() {
		cmd, args := msg.CommandWithArgs()
		cmdLower := strings.ToLower(cmd)

		switch cmdLower {
		case "new", "clear":
			return &Command{
				Identity:         identity,
				RequestID:        requestID,
				Type:             gateway.CommandNewConversation,
				RawText:          msg.Text,
				IsControlCommand: true,
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
			parts := strings.Fields(args)
			if len(parts) >= 2 {
				return &Command{
					Identity: identity,
					RequestID: requestID,
					Type:      gateway.CommandChangeProviderModel,
					Payload: gateway.ChangeProviderModelPayload{
						Provider: parts[0],
						Model:    strings.Join(parts[1:], " "),
					},
					RawText:          msg.Text,
					IsControlCommand: true,
				}, nil
			}
			if len(parts) == 1 {
				provider := strings.TrimSpace(parts[0])
				if _, ok := providers[provider]; !ok {
					return &Command{
						Identity:         identity,
						RequestID:        requestID,
						Type:             commandModelPickerError,
						Payload:          fmt.Sprintf("Unknown provider: %s", provider),
						RawText:          msg.Text,
						IsControlCommand: true,
					}, nil
				}
				return &Command{
					Identity:         identity,
					RequestID:        requestID,
					Type:             commandModelPickerProvider,
					Payload:          provider,
					RawText:          msg.Text,
					IsControlCommand: true,
				}, nil
			}
			return &Command{
				Identity:         identity,
				RequestID:        requestID,
				Type:             commandModelPickerOpen,
				RawText:          msg.Text,
				IsControlCommand: true,
			}, nil

		case "status":
			return &Command{
				Identity:  identity,
				RequestID: requestID,
				Type:      "status",
				RawText:   msg.Text,
			}, nil

		default:
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
