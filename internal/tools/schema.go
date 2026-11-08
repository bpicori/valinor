package tools

import (
	"encoding/json"
	"sync"
)

// Tool represents a tool with its schema definition and execution function.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	Exec        func(json.RawMessage) (string, error)
}

// RunContext carries per-run metadata needed by tools that emit client events.
type RunContext struct {
	mu         sync.RWMutex
	SessionID  string
	RequestID  string
	EmitEvents bool

	// fileSignatures tracks (size, mtime) per path for files the model has read,
	// so edit_file can refuse to rewrite a file that changed underneath it.
	fileSignatures map[string]string
}

// RunSnapshot is a point-in-time view of run metadata.
type RunSnapshot struct {
	SessionID  string
	RequestID  string
	EmitEvents bool
}

// Snapshot returns the current run metadata.
func (rc *RunContext) Snapshot() (RunSnapshot, bool) {
	if rc == nil {
		return RunSnapshot{}, false
	}
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	return RunSnapshot{
		SessionID:  rc.SessionID,
		RequestID:  rc.RequestID,
		EmitEvents: rc.EmitEvents,
	}, true
}

// Bind updates run metadata shared by tools built in the same bundle.
func (rc *RunContext) Bind(sessionID, requestID string, emitEvents bool) {
	if rc == nil {
		return
	}
	rc.mu.Lock()
	rc.SessionID = sessionID
	rc.RequestID = requestID
	rc.EmitEvents = emitEvents
	rc.mu.Unlock()
}

// RecordFileSignature remembers the signature of a file after read/write.
func (rc *RunContext) RecordFileSignature(path, signature string) {
	if rc == nil || path == "" || signature == "" {
		return
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.fileSignatures == nil {
		rc.fileSignatures = make(map[string]string)
	}
	rc.fileSignatures[path] = signature
}

// FileSignature returns the last recorded signature for a path.
func (rc *RunContext) FileSignature(path string) (string, bool) {
	if rc == nil {
		return "", false
	}
	rc.mu.RLock()
	defer rc.mu.RUnlock()
	sig, ok := rc.fileSignatures[path]
	return sig, ok
}

// Exclude returns tools whose names are not in exclude.
func Exclude(tools []Tool, names ...string) []Tool {
	excluded := make(map[string]struct{}, len(names))
	for _, name := range names {
		excluded[name] = struct{}{}
	}
	filtered := make([]Tool, 0, len(tools))
	for _, tool := range tools {
		if _, skip := excluded[tool.Name]; !skip {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}