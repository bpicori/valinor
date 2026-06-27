package config

import (
	"fmt"
	"sync"
)

// Paths holds resolved absolute directories computed at Init.
type Paths struct {
	ValinorDir   string
	SessionsDir  string
	WorkspaceDir string
	CronDir      string
	ScriptsDir   string
}

var (
	globalMu     sync.RWMutex
	globalCfg    Config
	globalPaths  Paths
	globalInited bool
)

// Init stores configuration and resolves application paths.
// Call once from main before any subsystem that uses Must or MustPaths.
func Init(cfg Config) error {
	valinorDir, err := cfg.ResolveValinorDir()
	if err != nil {
		return fmt.Errorf("resolve valinor dir: %w", err)
	}
	sessionsDir, err := cfg.ResolveSessionDir()
	if err != nil {
		return fmt.Errorf("resolve session dir: %w", err)
	}
	workspaceDir, err := cfg.ResolveWorkspaceDir()
	if err != nil {
		return fmt.Errorf("resolve workspace dir: %w", err)
	}
	cronDir, err := cfg.ResolveCronDir()
	if err != nil {
		return fmt.Errorf("resolve cron dir: %w", err)
	}
	scriptsDir, err := cfg.ResolveScriptsDir()
	if err != nil {
		return fmt.Errorf("resolve scripts dir: %w", err)
	}

	globalMu.Lock()
	defer globalMu.Unlock()
	globalCfg = cfg
	globalPaths = Paths{
		ValinorDir:   valinorDir,
		SessionsDir:  sessionsDir,
		WorkspaceDir: workspaceDir,
		CronDir:      cronDir,
		ScriptsDir:   scriptsDir,
	}
	globalInited = true
	return nil
}

// Must returns the process-global configuration.
func Must() Config {
	globalMu.RLock()
	defer globalMu.RUnlock()
	if !globalInited {
		panic("config: Init not called")
	}
	return globalCfg
}

// MustPaths returns resolved application directories.
func MustPaths() Paths {
	globalMu.RLock()
	defer globalMu.RUnlock()
	if !globalInited {
		panic("config: Init not called")
	}
	return globalPaths
}

// Get returns the process-global configuration and whether Init was called.
func Get() (Config, bool) {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return globalCfg, globalInited
}