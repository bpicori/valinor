package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

var (
	todoStore     *TodoStore
	todoStoreOnce sync.Once
)

func getTodoStore() *TodoStore {
	todoStoreOnce.Do(func() {
		todoStore = &TodoStore{
			items: make(map[string][]TodoItem),
		}
	})
	return todoStore
}

// TodoItem represents a single task in the todo list.
type TodoItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"` // pending, in_progress, completed, cancelled
}

// Valid statuses for a todo item.
const (
	StatusPending    = "pending"
	StatusInProgress = "in_progress"
	StatusCompleted  = "completed"
	StatusCancelled  = "cancelled"
)

func isValidStatus(status string) bool {
	switch status {
	case StatusPending, StatusInProgress, StatusCompleted, StatusCancelled:
		return true
	default:
		return false
	}
}

func formatStatus(status string) string {
	switch status {
	case StatusPending:
		return "pending"
	case StatusInProgress:
		return "in_progress"
	case StatusCompleted:
		return "completed"
	case StatusCancelled:
		return "cancelled"
	default:
		return status
	}
}

// TodoStore is an in-memory, session-scoped store for todo items.
type TodoStore struct {
	mu    sync.RWMutex
	items map[string][]TodoItem
}

func (s *TodoStore) Write(sessionID string, todos []TodoItem, merge bool) []TodoItem {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !merge {
		s.items[sessionID] = todos
		return s.cloneItems(todos)
	}

	existing := s.items[sessionID]
	existingMap := make(map[string]int)
	for i, item := range existing {
		if item.ID != "" {
			existingMap[item.ID] = i
		}
	}

	result := make([]TodoItem, len(existing))
	copy(result, existing)

	for _, newItem := range todos {
		if newItem.ID == "" {
			result = append(result, newItem)
			continue
		}
		if idx, exists := existingMap[newItem.ID]; exists {
			result[idx] = newItem
		} else {
			result = append(result, newItem)
		}
	}

	s.items[sessionID] = result
	return s.cloneItems(result)
}

func (s *TodoStore) Read(sessionID string) []TodoItem {
	s.mu.RLock()
	defer s.mu.RUnlock()

	items, exists := s.items[sessionID]
	if !exists {
		return []TodoItem{}
	}
	return s.cloneItems(items)
}

func (s *TodoStore) cloneItems(items []TodoItem) []TodoItem {
	result := make([]TodoItem, len(items))
	copy(result, items)
	return result
}

func formatTodos(todos []TodoItem) string {
	if len(todos) == 0 {
		return "No todos found."
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Task list (%d items):\n\n", len(todos)))

	for i, todo := range todos {
		status := formatStatus(todo.Status)
		sb.WriteString(fmt.Sprintf("%d. [%s] %s: %s\n", i+1, status, todo.ID, todo.Content))
	}

	return sb.String()
}

const (
	todoWriteName = "todo_write"
	todoReadName  = "todo_read"
)

func todoWriteParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"todos": map[string]any{
				"type":        "array",
				"description": "Array of todo items to create or update",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"id": map[string]any{
							"type":        "string",
							"description": "Unique identifier for this todo item",
						},
						"content": map[string]any{
							"type":        "string",
							"description": "Description of the task",
						},
						"status": map[string]any{
							"type":        "string",
							"description": "Current status of the task",
							"enum":        []string{StatusPending, StatusInProgress, StatusCompleted, StatusCancelled},
						},
					},
					"required":             []string{"id", "content", "status"},
					"additionalProperties": false,
				},
			},
			"merge": map[string]any{
				"type":        "boolean",
				"description": "If true, merge with existing todos by id (update matched, append new). If false, replace entire list.",
			},
		},
		"required":             []string{"todos", "merge"},
		"additionalProperties": false,
	}
}

func todoWriteExec(runCtx *RunContext, args json.RawMessage) (string, error) {
	snap, ok := runCtx.Snapshot()
	if !ok || snap.SessionID == "" {
		return "Error: no session context available", nil
	}

	var p struct {
		Todos []TodoItem `json:"todos"`
		Merge bool       `json:"merge"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	if len(p.Todos) == 0 && !p.Merge {
		return "Error: todos array must be non-empty when merge is false", nil
	}

	for i, todo := range p.Todos {
		if todo.ID == "" {
			return fmt.Sprintf("Error: todo at index %d is missing required 'id'", i), nil
		}
		if todo.Content == "" {
			return fmt.Sprintf("Error: todo at index %d is missing required 'content'", i), nil
		}
		if !isValidStatus(todo.Status) {
			return fmt.Sprintf("Error: todo at index %d has invalid status '%s' (must be: pending, in_progress, completed, cancelled)", i, todo.Status), nil
		}
	}

	result := getTodoStore().Write(snap.SessionID, p.Todos, p.Merge)
	return formatTodos(result), nil
}

func todoReadParams() map[string]any {
	return map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"required":             []string{},
		"additionalProperties": false,
	}
}

func todoReadExec(runCtx *RunContext, args json.RawMessage) (string, error) {
	snap, ok := runCtx.Snapshot()
	if !ok || snap.SessionID == "" {
		return "Error: no session context available", nil
	}

	todos := getTodoStore().Read(snap.SessionID)
	return formatTodos(todos), nil
}