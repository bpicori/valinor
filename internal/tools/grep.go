package tools

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

const maxGrepMatches = 100

const grepName = "grep"

func grepDef() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        grepName,
		Description: openai.String("Search for a regex pattern in files. Returns matching lines with file paths and line numbers."),
		Parameters: shared.FunctionParameters{
			"type": "object",
			"properties": map[string]any{
				"pattern": map[string]string{
					"type":        "string",
					"description": "Regular expression pattern to search for",
				},
				"path": map[string]string{
					"type":        "string",
					"description": "Relative path to search (file or directory, default \".\")",
				},
				"include": map[string]string{
					"type":        "string",
					"description": "Glob pattern to filter files (e.g. \"*.go\", \"*.txt\")",
				},
			},
			"required":             []string{"pattern"},
			"additionalProperties": false,
		},
	})
}

type grepMatch struct {
	file string
	line int
	text string
}

func grepExec(sb *Sandbox, args json.RawMessage) (string, error) {
	var p struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
		Include string `json:"include"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}

	if p.Pattern == "" {
		return "Tool Error: pattern is required", nil
	}

	re, err := regexp.Compile(p.Pattern)
	if err != nil {
		return "Tool Error: invalid regex: " + err.Error(), nil
	}

	path := normalizePath(p.Path)
	if path == "" {
		path = "."
	}
	if path != "." && (strings.HasPrefix(path, "..") || strings.Contains(path, "/..")) {
		return "Tool Error: path must not escape workspace (\"..\" not allowed)", nil
	}
	if filepath.IsAbs(path) {
		return "Tool Error: path must be relative to workspace (absolute paths not allowed)", nil
	}

	var matches []grepMatch
	err = fs.WalkDir(sb.root.FS(), path, func(filePath string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			return nil
		}

		// Filter by include pattern
		if p.Include != "" {
			matched, _ := filepath.Match(p.Include, d.Name())
			if !matched {
				return nil
			}
		}

		f, err := sb.root.Open(filePath)
		if err != nil {
			return nil
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				matches = append(matches, grepMatch{
					file: filePath,
					line: lineNum,
					text: line,
				})
				if len(matches) >= maxGrepMatches {
					return fs.SkipAll
				}
			}
		}
		return nil
	})

	if err != nil {
		return "Tool Error: " + err.Error(), nil
	}

	if len(matches) == 0 {
		return "No matches found.", nil
	}

	var b strings.Builder
	for _, m := range matches {
		b.WriteString(m.file)
		b.WriteString(":")
		b.WriteString(strconv.Itoa(m.line))
		b.WriteString(": ")
		b.WriteString(m.text)
		b.WriteString("\n")
	}

	if len(matches) >= maxGrepMatches {
		b.WriteString("\n(Results limited to 100 matches)\n")
	}

	return b.String(), nil
}
