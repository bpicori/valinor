package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"valinor/internal/sandbox"
)

var yearInQueryRE = regexp.MustCompile(`\b20\d{2}\b`)

const (
	webSearchName        = "web_search"
	webSearchTimeout     = 30 * time.Second
	webSearchSearxngPort = 8080
	webSearchMaxResults  = 12
)

const webSearchDescription = "Search the web via local SearXNG. Returns evidence snippets. " +
	"Write specific queries with entity names, full dates, and years for time-sensitive requests. " +
	"Disambiguate homonyms in the query. Use site: filters for authoritative sources. " +
	"Read the returned evidence and answer the user; cite source URLs. " +
	"Plan the query first; refine at most twice if results are off-topic."

func webSearchParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Specific search query. Include full dates/years for time-sensitive requests. Disambiguate entities (e.g. company name vs generic term). Use site:example.com when an authoritative source helps.",
			},
			"categories": map[string]any{
				"type":        "string",
				"description": "Optional SearXNG category: general, news, images, videos, science, it, files.",
			},
			"time_range": map[string]any{
				"type":        "string",
				"description": "Optional recency filter: day, month, year.",
				"enum":        []string{"day", "month", "year"},
			},
		},
		"required":             []string{"query"},
		"additionalProperties": false,
	}
}

func webSearchExec(rt *sandbox.Runtime, args json.RawMessage) (string, error) {
	var input struct {
		Query      string `json:"query"`
		Categories string `json:"categories"`
		TimeRange  string `json:"time_range"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	query := strings.TrimSpace(input.Query)
	if query == "" {
		return "Tool Error: 'query' is required and cannot be empty.", nil
	}

	now := time.Now()
	executedQuery := enrichQuery(query, now)
	evidence, err := fetchWebSearchEvidence(rt, executedQuery, input.Categories, input.TimeRange)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(evidence, "Tool Error:") {
		return evidence, nil
	}

	return formatWebSearchEvidence(evidence, query, executedQuery, now), nil
}

type searxResult struct {
	Title         string `json:"title"`
	URL           string `json:"url"`
	Content       string `json:"content"`
	PublishedDate string `json:"publishedDate"`
	Engine        string `json:"engine"`
}

func fetchWebSearchEvidence(rt *sandbox.Runtime, query, categories, timeRange string) (string, error) {
	params := url.Values{}
	params.Set("q", query)
	params.Set("format", "json")
	if cat := strings.TrimSpace(categories); cat != "" {
		params.Set("categories", cat)
	}
	if tr := strings.TrimSpace(timeRange); tr != "" {
		params.Set("time_range", tr)
	}

	searchURL := fmt.Sprintf("http://127.0.0.1:%d/search?%s", webSearchSearxngPort, params.Encode())
	ctx, cancel := context.WithTimeout(context.Background(), webSearchTimeout)
	defer cancel()

	curlCmd := fmt.Sprintf("curl -sf %q", searchURL)
	result, err := rt.RunCommand(ctx, ".", curlCmd, webSearchTimeout)
	if err != nil {
		return "", fmt.Errorf("web search request failed: %w", err)
	}
	if result.ExitCode != 0 {
		return formatWebSearchError(result), nil
	}
	return result.Stdout, nil
}

func formatWebSearchError(result sandbox.Result) string {
	msg := strings.TrimSpace(result.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(result.Stdout)
	}
	if result.ExitCode == 22 || strings.Contains(msg, "403") {
		return "Tool Error: SearXNG rejected the JSON search format. Rebuild the sandbox image with: valinor build-docker-image"
	}
	if strings.Contains(msg, "Connection refused") || strings.Contains(msg, "Failed to connect") {
		return "Tool Error: SearXNG is not running in the sandbox. Rebuild the sandbox image with: valinor build-docker-image"
	}
	if msg == "" {
		msg = "search request failed"
	}
	return "Tool Error: " + msg
}

func formatWebSearchEvidence(body, originalQuery, executedQuery string, now time.Time) string {
	var payload struct {
		Results []searxResult `json:"results"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return "Tool Error: failed to parse SearXNG response: " + err.Error()
	}
	if len(payload.Results) == 0 {
		return fmt.Sprintf(
			"No web results found.\nCurrent date: %s\nExecuted query: %s\nTry a more specific query with entity names, full date, site: filter, or categories=news.",
			now.Format("2006-01-02"),
			executedQuery,
		)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Current date: %s (%s)\n", now.Format("2006-01-02"), now.Format("Monday"))
	if executedQuery != originalQuery {
		fmt.Fprintf(&b, "Original query: %s\n", originalQuery)
	}
	fmt.Fprintf(&b, "Executed query: %s\n\n", executedQuery)

	limit := len(payload.Results)
	if limit > webSearchMaxResults {
		limit = webSearchMaxResults
	}
	for i := 0; i < limit; i++ {
		writeWebSearchResult(&b, i+1, payload.Results[i])
	}

	b.WriteString("Answer from these snippets only. Cite source URLs. Prefer snippets matching the current date; treat mismatched years as stale unless the user asked for history.\n")
	return strings.TrimRight(b.String(), "\n")
}

func writeWebSearchResult(b *strings.Builder, index int, item searxResult) {
	title := strings.TrimSpace(item.Title)
	if title == "" {
		title = strings.TrimSpace(item.URL)
	}
	if title == "" {
		return
	}
	fmt.Fprintf(b, "%d. %s\n", index, title)
	if uri := strings.TrimSpace(item.URL); uri != "" {
		fmt.Fprintf(b, "   %s\n", uri)
	}
	if published := strings.TrimSpace(item.PublishedDate); published != "" {
		fmt.Fprintf(b, "   published: %s\n", published)
	}
	if engine := strings.TrimSpace(item.Engine); engine != "" {
		fmt.Fprintf(b, "   engine: %s\n", engine)
	}
	if snippet := strings.TrimSpace(item.Content); snippet != "" {
		fmt.Fprintf(b, "   %s\n", snippet)
	}
	b.WriteString("\n")
}

func enrichQuery(query string, today time.Time) string {
	q := strings.TrimSpace(query)
	if q == "" {
		return q
	}

	lower := strings.ToLower(q)
	hasYear := yearInQueryRE.MatchString(q)
	dateLabel := today.Format("January 2 2006")

	if isTimeSensitive(lower) && !hasYear {
		q = q + " " + dateLabel
	}

	if !yearInQueryRE.MatchString(q) && containsDateFragment(q, today) {
		q = q + " " + today.Format("2006")
	}

	return strings.TrimSpace(q)
}

func isTimeSensitive(lower string) bool {
	for _, word := range []string{"today", "tonight", "this week", "right now", "current", "latest", "now"} {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

func containsDateFragment(query string, today time.Time) bool {
	lower := strings.ToLower(query)
	for _, fragment := range []string{
		today.Format("2006-01-02"),
		strings.ToLower(today.Format("January 2")),
		strings.ToLower(today.Format("Jan 2")),
	} {
		if strings.Contains(lower, strings.ToLower(fragment)) {
			return true
		}
	}
	return false
}