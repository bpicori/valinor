package tools

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

const listFilesName = "list_directory"

func listDirectoryMapError(err error, path string) string {
	if err == nil {
		return ""
	}
	name := filepath.Base(path)
	if name == "" || name == "." {
		name = path
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "Tool Error: The directory \"" + name + "\" does not exist in the workspace."
	case errors.Is(err, fs.ErrPermission), errors.Is(err, os.ErrPermission):
		return "Tool Error: Permission denied listing \"" + name + "\"."
	case errors.Is(err, os.ErrInvalid):
		return "Tool Error: Invalid path \"" + path + "\" (path escapes workspace or contains invalid characters)."
	default:
		return "Tool Error: " + err.Error()
	}
}

func listFilesDef() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        listFilesName,
		Description: openai.String("List files and directories at the given path. Path is relative to the workspace root. Use \".\" for the root. Directories are marked with a trailing slash."),
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"path": map[string]string{
					"type":        "string",
					"description": "Relative path to the directory (default \".\")",
				},
			},
			"required":             []string{},
			"additionalProperties": false,
		},
	})
}

func listFilesExec(sb *Sandbox, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	path := normalizePath(p.Path)

	rdfs, ok := sb.root.FS().(fs.ReadDirFS)
	if !ok {
		return "Tool Error: ReadDir not supported.", nil
	}

	entries, err := rdfs.ReadDir(path)
	if err != nil {
		return listDirectoryMapError(err, path), nil
	}

	// Clearly mark folders with trailing slash: ["data/", "notes.txt"]
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name = name + "/"
		}
		names = append(names, name)
	}

	data, _ := json.Marshal(names)
	return string(data), nil
}
