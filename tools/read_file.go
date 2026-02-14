package tools

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

const (
	readFileName   = "read_file"
	readFileMaxLen = 1e6 // 1MB limit to protect context window and memory
)

func readFileMapError(err error, path string) string {
	if err == nil {
		return ""
	}
	name := filepath.Base(path)
	if name == "" || name == "." {
		name = path
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "Tool Error: The file \"" + name + "\" does not exist in the workspace."
	case errors.Is(err, fs.ErrPermission), errors.Is(err, os.ErrPermission):
		return "Tool Error: Permission denied reading \"" + name + "\"."
	case errors.Is(err, os.ErrInvalid):
		return "Tool Error: Invalid path \"" + path + "\" (path escapes workspace or contains invalid characters)."
	default:
		return "Tool Error: " + err.Error()
	}
}

func readFileDef() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        readFileName,
		Description: openai.String("Read the contents of a file. Path is relative to the workspace root."),
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]string{
					"type":        "string",
					"description": "Relative path to the file",
				},
			},
			"required":             []string{"path"},
			"additionalProperties": false,
		},
	})
}

func readFileExec(sb *Sandbox, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	path := normalizePath(p.Path)

	f, err := sb.root.Open(path)
	if err != nil {
		return readFileMapError(err, path), nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return readFileMapError(err, path), nil
	}
	if info.IsDir() {
		return readFileMapError(fs.ErrInvalid, path), nil
	}

	limited := io.LimitReader(f, readFileMaxLen)
	data, err := io.ReadAll(limited)
	if err != nil {
		return readFileMapError(err, path), nil
	}
	return string(data), nil
}
