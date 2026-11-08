package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"valinor/internal/search"
)

const (
	webFetchName    = "web_fetch"
	webFetchTimeout = 60 * time.Second
)

const webFetchDescription = "Fetch a web page and return its title and content as Markdown. " +
	"Use it when search result summaries are not enough to answer accurately. " +
	"Pass an absolute http(s) URL taken from a search result or the user."

func webFetchParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "Absolute http:// or https:// URL to fetch.",
			},
		},
		"required":             []string{"url"},
		"additionalProperties": false,
	}
}

// buildWebFetchTool creates the web fetch tool for providers with fetch support.
func buildWebFetchTool(fetcher search.Fetcher) Tool {
	return buildTool(webFetchName, webFetchDescription, webFetchParams(), func(a json.RawMessage) (string, error) {
		return webFetchExec(fetcher, a)
	})
}

func webFetchExec(fetcher search.Fetcher, args json.RawMessage) (string, error) {
	var p struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	target := strings.TrimSpace(p.URL)
	if target == "" {
		return "Tool Error: 'url' is required and cannot be empty.", nil
	}
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		return "Tool Error: url must start with http:// or https://", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), webFetchTimeout)
	defer cancel()

	page, err := fetcher.Fetch(ctx, target)
	if err != nil {
		return "Tool Error: " + err.Error(), nil
	}

	var b strings.Builder
	if title := strings.TrimSpace(page.Title); title != "" {
		fmt.Fprintf(&b, "# %s\n\n", title)
	}
	fmt.Fprintf(&b, "%s\n\n", page.URL)
	b.WriteString(page.Content)
	return strings.TrimSpace(b.String()), nil
}
