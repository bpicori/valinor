package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Service defines the durable memory operations exposed to the rest of the app.
type Service interface {
	Save(ctx context.Context, input SaveInput) (Memory, error)
	Search(ctx context.Context, input SearchInput) ([]Memory, error)
	List(ctx context.Context, input ListInput) ([]Memory, error)
	Delete(ctx context.Context, workspaceDir, id string) (bool, error)
}

// Repository stores and retrieves durable memories.
type Repository interface {
	Save(ctx context.Context, memory Memory) (Memory, error)
	ListByWorkspace(ctx context.Context, workspaceDir string) ([]Memory, error)
	DeleteByID(ctx context.Context, id string) (bool, error)
	Touch(ctx context.Context, ids []string, touchedAt time.Time) error
}

// MemoryService implements workspace-scoped memory behavior on top of a repository.
type MemoryService struct {
	repo Repository
}

type scoredMemory struct {
	memory Memory
	score  int
}

// NewService creates a memory service.
func NewService(repo Repository) *MemoryService {
	return &MemoryService{repo: repo}
}

// Save validates input and persists a durable memory.
func (s *MemoryService) Save(ctx context.Context, input SaveInput) (Memory, error) {
	if s == nil || s.repo == nil {
		return Memory{}, fmt.Errorf("memory service is not configured")
	}
	workspaceDir := strings.TrimSpace(input.WorkspaceDir)
	if workspaceDir == "" {
		return Memory{}, fmt.Errorf("workspace_dir is required")
	}
	content := strings.TrimSpace(input.Content)
	if content == "" {
		return Memory{}, fmt.Errorf("content is required")
	}

	now := time.Now().UTC()
	memory := Memory{
		WorkspaceDir:    workspaceDir,
		Kind:            normalizeKind(input.Kind),
		Content:         content,
		Summary:         strings.TrimSpace(input.Summary),
		Tags:            normalizeTags(input.Tags),
		SourceSessionID: strings.TrimSpace(input.SourceSessionID),
		CreatedAt:       now,
		UpdatedAt:       now,
		LastAccessedAt:  now,
	}
	return s.repo.Save(ctx, memory)
}

// Search returns memories for a workspace ranked by a lightweight text/tag match.
func (s *MemoryService) Search(ctx context.Context, input SearchInput) ([]Memory, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("memory service is not configured")
	}
	workspaceDir := strings.TrimSpace(input.WorkspaceDir)
	if workspaceDir == "" {
		return nil, fmt.Errorf("workspace_dir is required")
	}

	memories, err := s.repo.ListByWorkspace(ctx, workspaceDir)
	if err != nil {
		return nil, err
	}

	query := strings.TrimSpace(input.Query)
	kind := normalizeKind(input.Kind)
	tags := normalizeTags(input.Tags)
	limit := normalizeLimit(input.Limit)

	scored := make([]scoredMemory, 0, len(memories))
	for _, memory := range memories {
		if kind != "" && memory.Kind != kind {
			continue
		}
		if len(tags) > 0 && !hasAnyTag(memory.Tags, tags) {
			continue
		}
		score := scoreMemory(memory, query, tags)
		if query != "" && score == 0 {
			continue
		}
		scored = append(scored, scoredMemory{memory: memory, score: score})
	}

	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		if !scored[i].memory.LastAccessedAt.Equal(scored[j].memory.LastAccessedAt) {
			return scored[i].memory.LastAccessedAt.After(scored[j].memory.LastAccessedAt)
		}
		return scored[i].memory.UpdatedAt.After(scored[j].memory.UpdatedAt)
	})

	if len(scored) > limit {
		scored = scored[:limit]
	}

	result := make([]Memory, 0, len(scored))
	touchedIDs := make([]string, 0, len(scored))
	for _, item := range scored {
		result = append(result, item.memory)
		touchedIDs = append(touchedIDs, item.memory.ID)
	}
	if len(touchedIDs) > 0 {
		_ = s.repo.Touch(ctx, touchedIDs, time.Now().UTC())
	}
	return result, nil
}

// List returns recent memories for a workspace with optional filters.
func (s *MemoryService) List(ctx context.Context, input ListInput) ([]Memory, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("memory service is not configured")
	}
	workspaceDir := strings.TrimSpace(input.WorkspaceDir)
	if workspaceDir == "" {
		return nil, fmt.Errorf("workspace_dir is required")
	}

	memories, err := s.repo.ListByWorkspace(ctx, workspaceDir)
	if err != nil {
		return nil, err
	}

	kind := normalizeKind(input.Kind)
	tags := normalizeTags(input.Tags)
	filtered := make([]Memory, 0, len(memories))
	for _, memory := range memories {
		if kind != "" && memory.Kind != kind {
			continue
		}
		if len(tags) > 0 && !hasAnyTag(memory.Tags, tags) {
			continue
		}
		filtered = append(filtered, memory)
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		if !filtered[i].UpdatedAt.Equal(filtered[j].UpdatedAt) {
			return filtered[i].UpdatedAt.After(filtered[j].UpdatedAt)
		}
		return filtered[i].CreatedAt.After(filtered[j].CreatedAt)
	})

	limit := normalizeLimit(input.Limit)
	if len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered, nil
}

// Delete removes a memory only if it belongs to the requested workspace.
func (s *MemoryService) Delete(ctx context.Context, workspaceDir, id string) (bool, error) {
	if s == nil || s.repo == nil {
		return false, fmt.Errorf("memory service is not configured")
	}
	workspaceDir = strings.TrimSpace(workspaceDir)
	id = strings.TrimSpace(id)
	if workspaceDir == "" {
		return false, fmt.Errorf("workspace_dir is required")
	}
	if id == "" {
		return false, fmt.Errorf("id is required")
	}

	memories, err := s.repo.ListByWorkspace(ctx, workspaceDir)
	if err != nil {
		return false, err
	}
	for _, memory := range memories {
		if memory.ID != id {
			continue
		}
		return s.repo.DeleteByID(ctx, id)
	}
	return false, nil
}

func normalizeKind(kind string) string {
	return strings.ToLower(strings.TrimSpace(kind))
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		normalized := strings.ToLower(strings.TrimSpace(tag))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func normalizeLimit(limit int) int {
	if limit <= 0 {
		return 10
	}
	if limit > 50 {
		return 50
	}
	return limit
}

func hasAnyTag(memoryTags, wanted []string) bool {
	for _, tag := range memoryTags {
		for _, expected := range wanted {
			if tag == expected {
				return true
			}
		}
	}
	return false
}

func scoreMemory(memory Memory, query string, tags []string) int {
	score := 0
	if len(tags) > 0 {
		for _, memoryTag := range memory.Tags {
			for _, wanted := range tags {
				if memoryTag == wanted {
					score += 15
				}
			}
		}
	}
	if query == "" {
		return score
	}

	lowerQuery := strings.ToLower(query)
	combined := strings.ToLower(strings.Join([]string{
		memory.Content,
		memory.Summary,
		memory.Kind,
		strings.Join(memory.Tags, " "),
	}, "\n"))

	if strings.EqualFold(memory.Content, query) {
		score += 100
	}
	if memory.Summary != "" && strings.EqualFold(memory.Summary, query) {
		score += 70
	}
	if strings.Contains(combined, lowerQuery) {
		score += 40
	}

	for _, token := range strings.Fields(lowerQuery) {
		if len(token) < 2 {
			continue
		}
		if strings.Contains(combined, token) {
			score += 10
		}
	}
	return score
}
