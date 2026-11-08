package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"valinor/internal/config"
	"valinor/internal/logger"
	"valinor/internal/sandbox"
	"valinor/internal/search"
	searchfactory "valinor/internal/search/factory"
)

// fileSignature returns a size+mtime signature for a workspace file.
// Tools run on the host and the workspace is bind-mounted into the sandbox,
// so a host stat reflects container writes.
func fileSignature(rt *sandbox.Runtime, path string) (string, error) {
	info, err := os.Stat(filepath.Join(rt.WorkspaceDir(), filepath.FromSlash(path)))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano()), nil
}

// normalizePath converts path separators for LLM-provided paths that may mix
// Windows and Unix separators, and cleans the path.
func normalizePath(path string) string {
	if path == "" {
		return "."
	}
	return filepath.ToSlash(filepath.Clean(path))
}

func validateRelativePath(path string) (string, string) {
	cleaned := normalizePath(path)
	if cleaned == "" {
		cleaned = "."
	}
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "/..") {
		logger.Warning("tool path validation failed", logger.BuildMetadata("path", path, "reason", "absolute or .. not allowed"))
		return "", "Tool Error: path must be relative to workspace (absolute paths and \"..\" not allowed)"
	}
	return cleaned, ""
}

func buildTool(name, description string, parameters map[string]any, exec func(json.RawMessage) (string, error)) Tool {
	paramsJSON, err := json.Marshal(parameters)
	if err != nil {
		logger.Error("tool definition marshal error", logger.BuildMetadata("tool", name, "error", err))
		paramsJSON = json.RawMessage("{}")
	}
	return Tool{
		Name:        name,
		Description: description,
		Parameters:  paramsJSON,
		Exec:        exec,
	}
}

// BindRunContext updates shared run metadata for tools built together.
func BindRunContext(run *RunContext, sessionID, requestID string, emitEvents bool) {
	if run == nil {
		return
	}
	run.Bind(sessionID, requestID, emitEvents)
}

// BuildTools returns base tools plus any provider-specific tools for the run.
func BuildTools(workspaceDir string, providerName, model string) ([]Tool, *RunContext, error) {
	_ = workspaceDir
	rt := sandbox.MustRuntime()

	cfg := config.Must()
	providerCfg, ok := cfg.Providers[providerName]
	if !ok {
		return nil, nil, fmt.Errorf("provider %q not found in config", providerName)
	}

	run := &RunContext{}
	result := []Tool{
		buildTool(readFileName, "Read a workspace file. Parameters: path, offset (optional), limit (optional).", readFileParams(), func(a json.RawMessage) (string, error) { return readFileExec(rt, run, a) }),
		buildTool(writeFileName, "Write a workspace file (overwrites). Parameters: path, content.", writeFileParams(), func(a json.RawMessage) (string, error) { return writeFileExec(rt, run, a) }),
		buildTool(editFileName, "Apply ordered text edits to a workspace file. Parameters: path, edits.", editFileParams(), func(a json.RawMessage) (string, error) { return editFileExec(rt, run, a) }),
		buildTool(listFilesName, "List workspace entries. Parameters: path (default \".\").", listFilesParams(), func(a json.RawMessage) (string, error) { return listFilesExec(rt, a) }),
		buildTool(grepName, "Search workspace files by regex. Parameters: pattern, path (optional), include (optional).", grepParams(), func(a json.RawMessage) (string, error) { return grepExec(rt, a) }),
		buildTool(pythonExecName, "Run Python in the sandbox. Parameters: code or script_path, cwd (optional).", pythonExecParams(), func(a json.RawMessage) (string, error) { return pythonExec(rt, a) }),
		buildTool(sendImageName, "Send a workspace image to the current client. Parameters: path.", sendImageParams(), func(a json.RawMessage) (string, error) { return sendImageExec(rt, run, a) }),
		buildTool(bashExecName, "Run bash in the sandbox. Parameters: command, cwd (optional).", bashExecParams(), func(a json.RawMessage) (string, error) { return bashExec(rt, a) }),
		buildTool(CronjobName,
			"Manage cron jobs. Parameters: action (add|list|remove|history|pause|resume|run|edit) plus job fields.",
			cronjobParams(), func(a json.RawMessage) (string, error) { return cronjobExec(run, a) }),
	}

	if reason := imageGenerationConfigError(providerCfg); reason == "" {
		result = append(result, buildTool(generateImageName, "Generate an image from a prompt. Saves to workspace. Parameters: prompt, send (default true), aspect_ratio (optional), size (optional).", generateImageParams(), func(a json.RawMessage) (string, error) { return generateImageExec(rt, run, providerCfg, a) }))
	} else {
		logger.Debug("image generation tool disabled", logger.BuildMetadata("provider", providerName, "reason", reason))
	}

	searchProvider, err := searchfactory.Create(cfg.Search)
	if err != nil {
		return nil, nil, err
	}
	if searchProvider != nil {
		result = append(result, buildWebSearchTool(searchProvider, cfg.Search.MaxResults))
		if fetcher, ok := searchProvider.(search.Fetcher); ok {
			result = append(result, buildWebFetchTool(fetcher))
		}
	} else if providerCfg.Type == "gemini" {
		searchTool, err := BuildGeminiGoogleSearchTool(providerCfg.APIKey, model)
		if err != nil {
			return nil, nil, err
		}
		result = append(result, searchTool)
	}

	names := make([]string, len(result))
	for i, tool := range result {
		names[i] = tool.Name
	}
	logger.Debug("tools built", logger.BuildMetadata("provider", providerName, "model", model, "count", len(result), "tools", strings.Join(names, ",")))

	return result, run, nil
}
