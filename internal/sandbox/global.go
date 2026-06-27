package sandbox

import (
	"fmt"
	"sync"

	"valinor/internal/config"
)

var (
	runtimeMu     sync.RWMutex
	globalRuntime *Runtime
)

// InitRuntime creates and stores the process-global sandbox runtime.
func InitRuntime(workspaceDir string, searxng config.SearXNGConfig) error {
	rt, err := NewRuntime(workspaceDir, searxng)
	if err != nil {
		return err
	}

	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if globalRuntime != nil {
		_ = globalRuntime.Close()
	}
	globalRuntime = rt
	return nil
}

// MustRuntime returns the process-global sandbox runtime.
func MustRuntime() *Runtime {
	runtimeMu.RLock()
	defer runtimeMu.RUnlock()
	if globalRuntime == nil {
		panic("sandbox: InitRuntime not called")
	}
	return globalRuntime
}

// CloseRuntime closes and clears the process-global sandbox runtime.
func CloseRuntime() error {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	if globalRuntime == nil {
		return nil
	}
	err := globalRuntime.Close()
	globalRuntime = nil
	if err != nil {
		return fmt.Errorf("close sandbox runtime: %w", err)
	}
	return nil
}