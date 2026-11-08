// Package deepseek implements web search and page fetching through the
// DeepSeek Search & Fetch API.
package deepseek

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"valinor/internal/config"
	"valinor/internal/logger"
	"valinor/internal/search"
)

const (
	defaultBaseURL   = "https://api.deepseek.com"
	searchTimeout    = 30 * time.Second
	fetchTimeout     = 60 * time.Second
	maxResponseBytes = 4 << 20
	maxErrorBytes    = 300
	maxQueryRunes    = 128
	maxResults       = 50
)

// Provider implements search.Provider and search.Fetcher for DeepSeek.
type Provider struct {
	client  *http.Client
	apiKey  string
	baseURL string
}

var (
	_ search.Provider = (*Provider)(nil)
	_ search.Fetcher  = (*Provider)(nil)
)

// New creates a DeepSeek search provider from configuration.
func New(cfg config.SearchConfig) (*Provider, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("deepseek search requires an api_key")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Provider{
		client:  &http.Client{},
		apiKey:  apiKey,
		baseURL: baseURL,
	}, nil
}

// Name returns the provider identifier.
func (p *Provider) Name() string {
	return "deepseek"
}

// Search queries DeepSeek's search endpoint and returns normalized results.
func (p *Provider) Search(ctx context.Context, req search.Request) ([]search.Result, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if utf8.RuneCountInString(query) > maxQueryRunes {
		return nil, fmt.Errorf("query must be at most %d characters", maxQueryRunes)
	}

	count := req.Count
	if count <= 0 {
		count = 10
	}
	if count > maxResults {
		count = maxResults
	}

	body := map[string]any{
		"query": query,
		"count": count,
	}
	if original := strings.TrimSpace(req.Original); original != "" && original != query {
		body["ori_question"] = truncateRunes(original, maxQueryRunes)
	}

	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	var payload struct {
		Results []searchResult `json:"results"`
	}
	if err := p.post(ctx, "/v1/tools/search", body, &payload); err != nil {
		return nil, err
	}

	out := make([]search.Result, 0, len(payload.Results))
	for _, item := range payload.Results {
		result := search.Result{
			Title:     item.Title,
			URL:       item.URL,
			Snippet:   item.Snippet,
			Summary:   item.Summary,
			SiteName:  derefString(item.SiteName),
			Score:     item.RerankScore,
			Authority: item.Authority,
		}
		if item.Date != nil {
			if published, err := time.Parse(time.RFC3339, *item.Date); err == nil {
				result.Published = published
			}
		}
		out = append(out, result)
	}

	logger.Debug("search completed", logger.BuildMetadata(
		"provider", "deepseek", "query", query, "results", len(out)))
	return out, nil
}

// Fetch retrieves one page as Markdown through DeepSeek's fetch endpoint.
func (p *Provider) Fetch(ctx context.Context, url string) (search.Page, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return search.Page{}, fmt.Errorf("url is required")
	}

	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()

	var payload struct {
		Title   string `json:"title"`
		Content string `json:"content"`
	}
	if err := p.post(ctx, "/v1/tools/fetch", map[string]any{"url": url}, &payload); err != nil {
		return search.Page{}, err
	}

	return search.Page{URL: url, Title: payload.Title, Content: payload.Content}, nil
}

type searchResult struct {
	RerankScore float64 `json:"rerank_score"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	Summary     string  `json:"summary"`
	SiteName    *string `json:"site_name"`
	Date        *string `json:"date"`
	Authority   int     `json:"authority"`
}

func (p *Provider) post(ctx context.Context, path string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return statusError(resp.StatusCode, respBody)
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}
	return nil
}

func statusError(status int, body []byte) error {
	excerpt := strings.TrimSpace(string(body))
	if len(excerpt) > maxErrorBytes {
		excerpt = excerpt[:maxErrorBytes] + "..."
	}
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("deepseek search rejected the api key (HTTP %d): %s", status, excerpt)
	case http.StatusTooManyRequests:
		return fmt.Errorf("deepseek search rate limited (5 QPS): %s", excerpt)
	default:
		return fmt.Errorf("deepseek search failed (HTTP %d): %s", status, excerpt)
	}
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}
