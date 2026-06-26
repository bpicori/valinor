package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"google.golang.org/genai"

)

const googleSearchName = "google_search"

const googleSearchTimeout = 30 * time.Second

// BuildGeminiGoogleSearchTool creates a Gemini-backed web search tool that can
// be safely used alongside normal function calling.
func BuildGeminiGoogleSearchTool(apiKey, model string) (Tool, error) {
	if strings.TrimSpace(apiKey) == "" {
		return Tool{}, fmt.Errorf("api key is required")
	}
	if strings.TrimSpace(model) == "" {
		return Tool{}, fmt.Errorf("model is required")
	}

	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:  apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return Tool{}, fmt.Errorf("create gemini client: %w", err)
	}

	return buildTool(
		googleSearchName,
		"Search the web using Gemini Google Search grounding. Use for current events, recent facts, external information, or anything that may require live web results. Returns a concise grounded answer with sources.",
		googleSearchParams(),
		func(args json.RawMessage) (string, error) {
			return googleSearchExec(client, model, args)
		},
	), nil
}

func googleSearchParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "The web search query or question to answer with grounded Google Search results.",
			},
		},
		"required":             []string{"query"},
		"additionalProperties": false,
	}
}

func googleSearchExec(client *genai.Client, model string, args json.RawMessage) (string, error) {
	var input struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	query := strings.TrimSpace(input.Query)
	if query == "" {
		return "Tool Error: 'query' is required and cannot be empty.", nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), googleSearchTimeout)
	defer cancel()

	prompt := fmt.Sprintf(
		"Use Google Search grounding to answer the user's request. Be concise, factual, and explicit about uncertainty.\n\nUser request: %s",
		query,
	)

	resp, err := client.Models.GenerateContent(
		ctx,
		model,
		[]*genai.Content{genai.NewContentFromText(prompt, genai.RoleUser)},
		&genai.GenerateContentConfig{
			Tools: []*genai.Tool{
				{GoogleSearch: &genai.GoogleSearch{}},
			},
		},
	)
	if err != nil {
		return "", fmt.Errorf("google search request failed: %w", err)
	}

	return formatGoogleSearchResponse(resp), nil
}

func formatGoogleSearchResponse(resp *genai.GenerateContentResponse) string {
	if resp == nil {
		return "No grounded answer returned."
	}

	answer := strings.TrimSpace(resp.Text())
	if answer == "" {
		answer = "No grounded answer returned."
	}

	if len(resp.Candidates) == 0 || resp.Candidates[0] == nil || resp.Candidates[0].GroundingMetadata == nil {
		return answer
	}

	metadata := resp.Candidates[0].GroundingMetadata
	sources := make([]string, 0, len(metadata.GroundingChunks))
	seen := make(map[string]struct{}, len(metadata.GroundingChunks))

	for _, chunk := range metadata.GroundingChunks {
		if chunk == nil || chunk.Web == nil {
			continue
		}
		uri := strings.TrimSpace(chunk.Web.URI)
		if uri == "" {
			continue
		}
		if _, ok := seen[uri]; ok {
			continue
		}
		seen[uri] = struct{}{}

		title := strings.TrimSpace(chunk.Web.Title)
		if title == "" {
			title = uri
		}
		sources = append(sources, fmt.Sprintf("- %s: %s", title, uri))
		if len(sources) >= 8 {
			break
		}
	}

	if len(sources) == 0 {
		return answer
	}

	return answer + "\n\nSources:\n" + strings.Join(sources, "\n")
}
