package tools

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

const writeFileName = "write_file"

func writeFileMapError(err error, path string) string {
	if err == nil {
		return ""
	}
	name := filepath.Base(path)
	if name == "" || name == "." {
		name = path
	}
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "Tool Error: The path \"" + name + "\" does not exist in the workspace."
	case errors.Is(err, os.ErrPermission):
		return "Tool Error: Permission denied writing to \"" + name + "\"."
	case errors.Is(err, os.ErrInvalid):
		return "Tool Error: Invalid path \"" + path + "\" (path escapes workspace or contains invalid characters)."
	case errors.Is(err, os.ErrExist):
		return "Tool Error: \"" + name + "\" already exists."
	default:
		return "Tool Error: " + err.Error()
	}
}

func writeFileDef() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        writeFileName,
		Description: openai.String("Write content to a file. Path is relative to the workspace root. Creates parent directories if needed. Overwrites existing files."),
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]string{
					"type":        "string",
					"description": "Relative path to the file",
				},
				"content": map[string]string{
					"type":        "string",
					"description": "Content to write",
				},
			},
			"required":             []string{"path", "content"},
			"additionalProperties": false,
		},
	})
}

// mkdirAllWithinRoot creates all parent directories for path within the root.
func mkdirAllWithinRoot(root *os.Root, path string) error {
	dir := filepath.ToSlash(filepath.Dir(path))
	if dir == "" || dir == "." {
		return nil
	}
	parts := strings.Split(dir, "/")
	current := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if current == "" {
			current = p
		} else {
			current = current + "/" + p
		}
		if err := root.Mkdir(current, 0755); err != nil && !os.IsExist(err) {
			return err
		}
	}
	return nil
}

func writeFileExec(sb *Sandbox, args json.RawMessage) (string, error) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	path := normalizePath(p.Path)

	sb.mu.Lock()
	defer sb.mu.Unlock()

	if err := mkdirAllWithinRoot(sb.root, path); err != nil {
		return writeFileMapError(err, path), nil
	}

	// Use OpenFile with 0644 for correct permissions (Create uses 0666)
	f, err := sb.root.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return writeFileMapError(err, path), nil
	}
	defer f.Close()

	if _, err := io.Copy(f, strings.NewReader(p.Content)); err != nil {
		return writeFileMapError(err, path), nil
	}
	return "ok", nil
}
