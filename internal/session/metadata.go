package session

import "time"

// Metadata holds session metadata persisted in metadata.json.
type Metadata struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	WorkspaceDir string    `json:"workspace_dir"`
}
