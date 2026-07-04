package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	DefaultRepo    = "bpicori/valinor"
	DefaultMode    = "all"
	BinaryName     = "valinor"
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

// DefaultPaths resolves deployment paths for the current user.
func DefaultPaths() (Paths, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("resolve home directory: %w", err)
	}

	binDir := filepath.Join(home, ".local", "bin")
	logDir := filepath.Join(home, ".valinor", "logs")

	return Paths{
		Home:       home,
		Binary:     filepath.Join(binDir, BinaryName),
		PrevBinary: filepath.Join(binDir, BinaryName+".prev"),
		ConfigFile: filepath.Join(home, ".valinor", "config.toml"),
		AppLog:     filepath.Join(logDir, "valinor.log"),
		DeployLog:  filepath.Join(logDir, "deploy.log"),
	}, nil
}

// AppendDeployLog writes a timestamped line to the deploy log.
func AppendDeployLog(path, message string) error {
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