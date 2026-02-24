package applog

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	logDirName  = "logs"
	logFileName = "valinor.log"
)

// Level is the log severity.
type Level string

const (
	LevelDebug   Level = "debug"
	LevelInfo    Level = "info"
	LevelWarning Level = "warning"
	LevelError   Level = "error"
)

type TransportType string

const (
	TransportFile   TransportType = "file"
	TransportStdout TransportType = "stdout"
)

var (
	mu     sync.Mutex
	output io.Writer = os.Stderr
)

// Init configures the app logger with specified transports.
func Init(valinorDir string, transports []TransportType) (logPath string, closeFn func() error, err error) {
	var writers []io.Writer
	var file *os.File

	hasFile, hasStdout := transportTypes(transports)

	if hasFile {
		// ensure the log directory exists
		targetDir := filepath.Join(valinorDir, logDirName)
		if err := os.MkdirAll(targetDir, 0o700); err != nil {
			return "", nil, err
		}

		logPath = filepath.Join(targetDir, logFileName)
		file, err = os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return "", nil, err
		}
		writers = append(writers, file)
	}

	if hasStdout {
		writers = append(writers, os.Stdout)
	}

	if len(writers) == 0 {
		// Default to stderr if no transports specified
		writers = append(writers, os.Stderr)
	}

	mu.Lock()
	if len(writers) == 1 {
		output = writers[0]
	} else {
		output = io.MultiWriter(writers...)
	}
	mu.Unlock()

	closeFn = func() error {
		if file != nil {
			return file.Close()
		}
		return nil
	}

	return logPath, closeFn, nil
}

// Log writes a structured log line: [LEVEL][MESSAGE][METADATA_JSON]
// Message must be a static string; dynamic values go in metadata.
func Log(level Level, msg string, metadata map[string]any) {
	meta := normalizeMetadata(metadata)
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		metaJSON = []byte(`{"_marshal_error":"` + err.Error() + `"}`)
	}
	ts := time.Now().UTC().Format("2006-01-02 15:04:05.000000")
	line := fmt.Sprintf("%s [%s][%s][%s]\n", ts, level, msg, string(metaJSON))

	mu.Lock()
	_, _ = output.Write([]byte(line))
	mu.Unlock()
}

// Debug logs at debug level.
func Debug(msg string, metadata map[string]any) {
	Log(LevelDebug, msg, metadata)
}

// Info logs at info level.
func Info(msg string, metadata map[string]any) {
	Log(LevelInfo, msg, metadata)
}

// Warning logs at warning level.
func Warning(msg string, metadata map[string]any) {
	Log(LevelWarning, msg, metadata)
}

// Error logs at error level.
func Error(msg string, metadata map[string]any) {
	Log(LevelError, msg, metadata)
}

// normalizeMetadata converts metadata to a safe map for JSON serialization.
// Errors are converted to their string representation.
func normalizeMetadata(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if err, ok := v.(error); ok {
			out[k] = err.Error()
		} else {
			out[k] = v
		}
	}
	return out
}

// M is a convenience for building metadata. Use as: applog.Info("message", applog.M("session_id", id, "error", err))
func M(kv ...any) map[string]any {
	if len(kv)%2 != 0 {
		return map[string]any{"_odd_kv": fmt.Sprintf("%d", len(kv))}
	}
	m := make(map[string]any, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			key = fmt.Sprintf("%v", kv[i])
		}
		m[key] = kv[i+1]
	}
	return m
}

func transportTypes(transports []TransportType) (bool, bool) {
	hasFile := false
	hasStdout := false
	for _, t := range transports {
		if t == TransportFile {
			hasFile = true
		}
	}
	return hasFile, hasStdout
}
