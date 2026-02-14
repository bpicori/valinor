package tools

import (
	"os"
	"sync"
)

// Sandbox holds the os.Root for directory-limited file access and a mutex
// for safe concurrent tool execution.
type Sandbox struct {
	root *os.Root
	mu   sync.Mutex
}

// NewSandbox opens a root directory for the agent workspace.
// The directory must exist; create it with os.MkdirAll before calling.
func NewSandbox(dir string) (*Sandbox, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	return &Sandbox{root: root}, nil
}

// Close releases the root. Call when the sandbox is no longer needed.
func (s *Sandbox) Close() error {
	if s.root != nil {
		err := s.root.Close()
		s.root = nil
		return err
	}
	return nil
}
