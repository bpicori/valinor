package memory

import "time"

// Memory stores a durable workspace-scoped fact the agent can recall later.
type Memory struct {
	ID              string
	WorkspaceDir    string
	Kind            string
	Content         string
	Summary         string
	Tags            []string
	SourceSessionID string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	LastAccessedAt  time.Time
}

// SaveInput contains the fields required to persist a memory.
type SaveInput struct {
	WorkspaceDir    string
	Kind            string
	Content         string
	Summary         string
	Tags            []string
	SourceSessionID string
}

// SearchInput describes a memory search request.
type SearchInput struct {
	WorkspaceDir string
	Query        string
	Kind         string
	Tags         []string
	Limit        int
}

// ListInput describes a filtered memory listing request.
type ListInput struct {
	WorkspaceDir string
	Kind         string
	Tags         []string
	Limit        int
}
