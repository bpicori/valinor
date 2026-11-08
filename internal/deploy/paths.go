package deploy

import (
	"fmt"
	"os"
	"path/filepath"

	"valinor/internal/config"
	"valinor/internal/logger"
)

const (
	DefaultRepo = "bpicori/valinor"
	BinaryName  = "valinor"
)

// Paths holds canonical deployment paths under the user home directory.
type Paths struct {
	Instance    string
	Home        string
	Binary      string
	PrevBinary  string
	ConfigFile  string
	DataDir     string
	AppLog      string
	ServiceUnit string
}

func pathsForInstance(name string) (Paths, error) {
	instance, err := config.ResolveInstance(name)
	if err != nil {
		return Paths{}, err
	}
	cfg, err := config.LoadForInstance(instance.Name)
	if err != nil {
		return Paths{}, err
	}
	dataDir, err := cfg.ResolveValinorDir()
	if err != nil {
		return Paths{}, err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}
	binDir := filepath.Join(home, ".local", "bin")
	return Paths{
		Instance:    instance.Name,
		Home:        home,
		Binary:      filepath.Join(binDir, BinaryName),
		PrevBinary:  filepath.Join(binDir, BinaryName+".prev"),
		ConfigFile:  instance.ConfigPath,
		DataDir:     dataDir,
		AppLog:      logger.AppLogPath(dataDir),
		ServiceUnit: "valinor@" + instance.Name + ".service",
	}, nil
}

func defaultPaths() (Paths, error) {
	return pathsForInstance(config.DefaultInstanceName)
}
