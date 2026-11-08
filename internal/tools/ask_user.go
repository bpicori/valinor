package tools

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"valinor/internal/event"
)

const (
	// AskUserName is exported so scheduled agent runs can exclude it.
	AskUserName       = "ask_user"
	maxAskOptions     = 6
	maxAskQuestionLen = 3500
)

func askUserParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"question": map[string]string{
				"type":        "string",
				"description": "The question to ask the user.",
			},
			"options": map[string]any{
				"type":        "array",
				"description": "Optional answer buttons (2-6). Omit for a free-text question.",
				"items":       map[string]string{"type": "string"},
				"minItems":    2,
				"maxItems":    maxAskOptions,
			},
			"allow_free_text": map[string]any{
				"type":        "boolean",
				"description": "Allow a typed answer in addition to the options (default true).",
			},
		},
		"required":             []string{"question"},
		"additionalProperties": false,
	}
}

func askUserExec(run *RunContext, args json.RawMessage) (ToolResult, error) {
	var p struct {
		Question      string   `json:"question"`
		Options       []string `json:"options"`
		AllowFreeText *bool    `json:"allow_free_text"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return ToolResult{}, err
	}

	question := strings.TrimSpace(p.Question)
	if question == "" {
		return ToolResult{Text: "Tool Error: 'question' is required."}, nil
	}
	if utf8.RuneCountInString(question) > maxAskQuestionLen {
		return ToolResult{Text: fmt.Sprintf("Tool Error: question is longer than %d characters.", maxAskQuestionLen)}, nil
	}

	options, optionErr := normalizeQuestionOptions(p.Options)
	if optionErr != "" {
		return ToolResult{Text: optionErr}, nil
	}

	allowFreeText := true
	if p.AllowFreeText != nil {
		allowFreeText = *p.AllowFreeText
	}
	if len(options) == 0 && !allowFreeText {
		return ToolResult{Text: "Tool Error: provide 2-6 options or allow_free_text."}, nil
	}

	snapshot, ok := run.Snapshot()
	if !ok || !snapshot.EmitEvents || snapshot.SessionID == "" {
		return ToolResult{Text: "Tool Error: ask_user requires an interactive client."}, nil
	}

	questionID, err := newQuestionID()
	if err != nil {
		return ToolResult{}, fmt.Errorf("generate question id: %w", err)
	}

	event.Publish(event.Event{
		Type: event.AgentQuestion,
		Payload: event.QuestionPayload{
			QuestionID:    questionID,
			Question:      question,
			Options:       options,
			AllowFreeText: allowFreeText,
		},
		SessionID: snapshot.SessionID,
		RequestID: snapshot.RequestID,
	})

	return ToolResult{
		Text:    "Question sent to the user. End your turn and wait for the answer; do not guess. The answer will arrive as the next user message.",
		EndTurn: true,
	}, nil
}

func normalizeQuestionOptions(raw []string) ([]string, string) {
	if len(raw) == 0 {
		return nil, ""
	}
	if len(raw) > maxAskOptions {
		return nil, fmt.Sprintf("Tool Error: at most %d options are allowed.", maxAskOptions)
	}

	seen := make(map[string]struct{}, len(raw))
	options := make([]string, 0, len(raw))
	for i, option := range raw {
		option = strings.TrimSpace(option)
		if option == "" {
			return nil, fmt.Sprintf("Tool Error: options[%d] is empty.", i)
		}
		if _, ok := seen[option]; ok {
			return nil, fmt.Sprintf("Tool Error: options[%d] is a duplicate.", i)
		}
		seen[option] = struct{}{}
		options = append(options, option)
	}
	if len(options) < 2 {
		return nil, "Tool Error: provide at least two options, or omit options for a free-text question."
	}
	return options, ""
}

func newQuestionID() (string, error) {
	data := make([]byte, 6)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}
