package logger

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
	LogDirName  = "logs"
	LogFileName = "valinor.log"
)

// AppLogPath returns the default application log path under valinorDir.
func AppLogPath(valinorDir string) string {
	return filepath.Join(valinorDir, LogDirName, LogFileName)
}

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
	prefix string
)

// Init configures the app logger with specified transports.
func Init(valinorDir string, transports []TransportType) (logPath string, closeFn func() error, err error) {
	var writers []io.Writer
	var file *os.File

	hasFile, hasStdout := transportTypes(transports)

	if hasFile {
		// ensure the log directory exists
		targetDir := filepath.Join(valinorDir, LogDirName)
		if err := os.MkdirAll(targetDir, 0o700); err != nil {
			return "", nil, err
		}

		logPath = filepath.Join(targetDir, LogFileName)
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

// SetPrefix sets a prefix that will be included in log messages (e.g., "GATEWAY", "TUI").
func SetPrefix(p string) {
	mu.Lock()
	prefix = p
	mu.Unlock()
}

// Log writes a JSON-lines log record.
// Message must be a static string; dynamic values go in metadata.
func Log(level Level, msg string, metadata map[string]any) {
	entry := buildLogEntry(level, msg, metadata)
	line, err := json.Marshal(entry)
	if err != nil {
		line = []byte(fmt.Sprintf(
			`{"timestamp":%q,"level":"error","message":"log marshal failed","marshal_error":%q}`+"\n",
			time.Now().UTC().Format(time.RFC3339Nano),
			err.Error(),
		))
	} else {
		line = append(line, '\n')
	}

	mu.Lock()
	_, _ = output.Write(line)
	mu.Unlock()
}

func buildLogEntry(level Level, msg string, metadata map[string]any) map[string]any {
	meta := normalizeMetadata(metadata)
	entry := make(map[string]any, len(meta)+4)
	entry["timestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
	entry["level"] = string(level)
	entry["message"] = msg

	mu.Lock()
	p := prefix
	mu.Unlock()

	if p != "" {
		entry["prefix"] = p
	}

	for k, v := range meta {
		if reservedLogKey(k) {
			entry["_"+k] = v
			continue
		}
		entry[k] = v
	}

	return entry
}

func reservedLogKey(key string) bool {
	switch key {
	case "timestamp", "level", "message", "prefix":
		return true
	default:
		return false
	}
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

// BuildMetadata is a convenience for building metadata. Use as: applog.Info("message", applog.BuildMetadata("session_id", id, "error", err))
func BuildMetadata(kv ...any) map[string]any {
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
		switch t {
		case TransportFile:
			hasFile = true
		case TransportStdout:
			hasStdout = true
		}
	}
	return hasFile, hasStdout
}