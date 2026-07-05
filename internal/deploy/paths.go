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
	ServiceName = "valinor"
)

// Paths holds canonical deployment paths under the user home directory.
type Paths struct {
	Home       string
	Binary     string
	PrevBinary string
	ConfigFile string
	AppLog     string
}

func defaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}

	configFile, err := config.DefaultConfigPath()
	if err != nil {
		return Paths{}, err
	}

	binDir := filepath.Join(home, ".local", "bin")
	valinorDir := filepath.Dir(configFile)

	return Paths{
		Home:       home,
		Binary:     filepath.Join(binDir, BinaryName),
		PrevBinary: filepath.Join(binDir, BinaryName+".prev"),
		ConfigFile: configFile,
		AppLog:     logger.AppLogPath(valinorDir),
	}, nil
}