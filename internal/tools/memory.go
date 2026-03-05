package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"valinor/internal/memory"
)

const (
	memorySaveName   = "memory_save"
	memorySearchName = "memory_search"
	memoryListName   = "memory_list"
	memoryDeleteName = "memory_delete"
)

func buildMemoryTools(service memory.Service, workspaceDir, sessionID string) []Tool {
	return []Tool{
		buildTool(
			memorySaveName,
			"Save a durable workspace memory when the information is likely to matter in future conversations, such as preferences, project conventions, constraints, or important facts. Do not store secrets or temporary tool output.",
			memorySaveParams(),
			func(args json.RawMessage) (string, error) {
				return memorySaveExec(service, workspaceDir, sessionID, args)
			},
		),
		buildTool(
			memorySearchName,
			"Search workspace memories before answering questions that depend on prior preferences, project decisions, recurring constraints, or facts saved earlier.",
			memorySearchParams(),
			func(args json.RawMessage) (string, error) {
				return memorySearchExec(service, workspaceDir, args)
			},
		),
		buildTool(
			memoryListName,
			"List stored workspace memories, optionally filtered by kind or tags. Use when you need to inspect what durable context is already saved.",
			memoryListParams(),
			func(args json.RawMessage) (string, error) {
				return memoryListExec(service, workspaceDir, args)
			},
		),
		buildTool(
			memoryDeleteName,
			"Delete an incorrect or outdated workspace memory by ID to avoid stale or contradictory future recall.",
			memoryDeleteParams(),
			func(args json.RawMessage) (string, error) {
				return memoryDeleteExec(service, workspaceDir, args)
			},
		),
	}
}

func memorySaveParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{
				"type":        "string",
				"description": "The durable fact to remember. Keep it concise and useful for future conversations.",
			},
			"kind": map[string]any{
				"type":        "string",
				"description": "Optional category such as preference, constraint, identity, project_note, fact, or workflow.",
			},
			"summary": map[string]any{
				"type":        "string",
				"description": "Optional short summary for faster scanning later.",
			},
			"tags": map[string]any{
				"type":        "array",
				"description": "Optional tags that make later retrieval easier.",
				"items": map[string]any{
					"type": "string",
				},
			},
		},
		"required":             []string{"content"},
		"additionalProperties": false,
	}
}

func memorySearchParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Search text describing the memory to recall.",
			},
			"kind": map[string]any{
				"type":        "string",
				"description": "Optional category filter.",
			},
			"tags": map[string]any{
				"type":        "array",
				"description": "Optional tags to narrow results.",
				"items": map[string]any{
					"type": "string",
				},
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum results to return. Default 5, max 50.",
			},
		},
		"required":             []string{"query"},
		"additionalProperties": false,
	}
}

func memoryListParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind": map[string]any{
				"type":        "string",
				"description": "Optional category filter.",
			},
			"tags": map[string]any{
				"type":        "array",
				"description": "Optional tags to narrow results.",
				"items": map[string]any{
					"type": "string",
				},
			},
			"limit": map[string]any{
				"type":        "integer",
				"description": "Maximum results to return. Default 10, max 50.",
			},
		},
		"required":             []string{},
		"additionalProperties": false,
	}
}

func memoryDeleteParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"memory_id": map[string]any{
				"type":        "string",
				"description": "The ID of the memory to delete.",
			},
		},
		"required":             []string{"memory_id"},
		"additionalProperties": false,
	}
}

func memorySaveExec(service memory.Service, workspaceDir, sessionID string, args json.RawMessage) (string, error) {
	var input struct {
		Content string   `json:"content"`
		Kind    string   `json:"kind"`
		Summary string   `json:"summary"`
		Tags    []string `json:"tags"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	saved, err := service.Save(context.Background(), memory.SaveInput{
		WorkspaceDir:    workspaceDir,
		Kind:            input.Kind,
		Content:         input.Content,
		Summary:         input.Summary,
		Tags:            input.Tags,
		SourceSessionID: sessionID,
	})
	if err != nil {
		return "", err
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("Saved memory %s", saved.ID))
	if saved.Kind != "" {
		parts = append(parts, "kind="+saved.Kind)
	}
	if len(saved.Tags) > 0 {
		parts = append(parts, "tags="+strings.Join(saved.Tags, ", "))
	}
	return strings.Join(parts, " | "), nil
}

func memorySearchExec(service memory.Service, workspaceDir string, args json.RawMessage) (string, error) {
	var input struct {
		Query string   `json:"query"`
		Kind  string   `json:"kind"`
		Tags  []string `json:"tags"`
		Limit int      `json:"limit"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(input.Query) == "" {
		return "Error: query is required", nil
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 5
	}
	memories, err := service.Search(context.Background(), memory.SearchInput{
		WorkspaceDir: workspaceDir,
		Query:        input.Query,
		Kind:         input.Kind,
		Tags:         input.Tags,
		Limit:        limit,
	})
	if err != nil {
		return "", err
	}
	return formatMemories(memories, "No matching memories found."), nil
}

func memoryListExec(service memory.Service, workspaceDir string, args json.RawMessage) (string, error) {
	var input struct {
		Kind  string   `json:"kind"`
		Tags  []string `json:"tags"`
		Limit int      `json:"limit"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	memories, err := service.List(context.Background(), memory.ListInput{
		WorkspaceDir: workspaceDir,
		Kind:         input.Kind,
		Tags:         input.Tags,
		Limit:        input.Limit,
	})
	if err != nil {
		return "", err
	}
	return formatMemories(memories, "No memories stored yet."), nil
}

func memoryDeleteExec(service memory.Service, workspaceDir string, args json.RawMessage) (string, error) {
	var input struct {
		MemoryID string `json:"memory_id"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(input.MemoryID) == "" {
		return "Error: memory_id is required", nil
	}

	deleted, err := service.Delete(context.Background(), workspaceDir, input.MemoryID)
	if err != nil {
		return "", err
	}
	if !deleted {
		return fmt.Sprintf("No memory found with ID %s in this workspace.", input.MemoryID), nil
	}
	return fmt.Sprintf("Deleted memory %s.", input.MemoryID), nil
}

func formatMemories(memories []memory.Memory, emptyMessage string) string {
	if len(memories) == 0 {
		return emptyMessage
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Found %d memory item(s):\n\n", len(memories)))
	for _, item := range memories {
		sb.WriteString(fmt.Sprintf("- ID: %s\n", item.ID))
		if item.Kind != "" {
			sb.WriteString(fmt.Sprintf("  Kind: %s\n", item.Kind))
		}
		if item.Summary != "" {
			sb.WriteString(fmt.Sprintf("  Summary: %s\n", item.Summary))
		}
		sb.WriteString(fmt.Sprintf("  Content: %s\n", item.Content))
		if len(item.Tags) > 0 {
			sb.WriteString(fmt.Sprintf("  Tags: %s\n", strings.Join(item.Tags, ", ")))
		}
		sb.WriteString(fmt.Sprintf("  Updated: %s\n\n", item.UpdatedAt.Format("2006-01-02 15:04 MST")))
	}
	return sb.String()
}
