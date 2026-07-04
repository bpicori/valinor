package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"valinor/internal/config"
	"valinor/internal/logger"
)

const (
	DefaultRepo = "bpicori/valinor"
	BinaryName  = "valinor"
	ServiceName    = "valinor"
	UpdateInterval = "30 minutes"
)

// Paths holds canonical deployment paths under the user home directory.
type Paths struct {
	Home       string
	Binary     string
	PrevBinary string
	ConfigFile string
	AppLog     string
	DeployLog  string
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
	logDir := filepath.Join(valinorDir, logger.LogDirName)

	return Paths{
		Home:       home,
		Binary:     filepath.Join(binDir, BinaryName),
		PrevBinary: filepath.Join(binDir, BinaryName+".prev"),
		ConfigFile: configFile,
		AppLog:     logger.AppLogPath(valinorDir),
		DeployLog:  filepath.Join(logDir, "deploy.log"),
	}, nil
}

func appendDeployLog(path, message string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create deploy log directory: %w", err)
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open deploy log: %w", err)
	}
	defer file.Close()

	timestamp := time.Now().UTC().Format(time.RFC3339)
	if _, err := fmt.Fprintf(file, "[%s] %s\n", timestamp, message); err != nil {
		return fmt.Errorf("write deploy log: %w", err)
	}
	return nil
}