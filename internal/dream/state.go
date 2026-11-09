package dream

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	agentcontext "valinor/internal/context"
)

const (
	stateVersion   = 1
	stateFileName  = "state.json"
	backupsDirName = "backups"
)

// sessionMark records the last consolidated transcript digest for one session.
type sessionMark struct {
	DigestHash string    `json:"digest_hash"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// state is the persisted dreaming watermark and schedule.
type state struct {
	Version             int                    `json:"version"`
	LastAttemptAt       time.Time              `json:"last_attempt_at"`
	LastSuccessAt       time.Time              `json:"last_success_at"`
	NextAttemptAt       time.Time              `json:"next_attempt_at"`
	ConsecutiveFailures int                    `json:"consecutive_failures"`
	LastError           string                 `json:"last_error,omitempty"`
	Sessions            map[string]sessionMark `json:"sessions"`
}

func newState() *state {
	return &state{
		Version:  stateVersion,
		Sessions: make(map[string]sessionMark),
	}
}

func loadState(path string) (*state, error) {
	data, err := readFileIfExists(path)
	if err != nil {
		return nil, fmt.Errorf("read state: %w", err)
	}
	if len(data) == 0 {
		return newState(), nil
	}

	var saved state
	if err := json.Unmarshal(data, &saved); err != nil {
		return nil, fmt.Errorf("decode state: %w", err)
	}
	if saved.Sessions == nil {
		saved.Sessions = make(map[string]sessionMark)
	}
	saved.Version = stateVersion
	return &saved, nil
}

func (s *state) save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	return writeFileAtomic(path, data, 0o600)
}

// pruneSessions drops marks for sessions that no longer exist.
func (s *state) pruneSessions(existing map[string]struct{}) {
	for id := range s.Sessions {
		if _, ok := existing[id]; !ok {
			delete(s.Sessions, id)
		}
	}
}

func statePath(dir string) string  { return filepath.Join(dir, stateFileName) }
func backupsDir(dir string) string { return filepath.Join(dir, backupsDirName) }

// memoryPath returns the consolidated memory file for a workspace.
func memoryPath(workspaceDir string) string {
	return filepath.Join(workspaceDir, agentcontext.MemoryFile)
}
