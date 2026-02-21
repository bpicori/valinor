package applog

import (
	"log"
	"os"
	"path/filepath"
)

const (
	logDirName  = "logs"
	logFileName = "valinor.log"
)

// Init configures the standard logger to write to stderr and a file.
func Init(valinorDir string) (logPath string, closeFn func() error, err error) {
	targetDir := filepath.Join(valinorDir, logDirName)
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		return "", nil, err
	}

	logPath = filepath.Join(targetDir, logFileName)
	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", nil, err
	}

	log.SetOutput(file)
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.LUTC)

	return logPath, file.Close, nil
}
