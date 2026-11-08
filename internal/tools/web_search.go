package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"valinor/internal/search"
)

var yearInQueryRE = regexp.MustCompile(`\b20\d{2}\b`)

const (
	webSearchName          = "web_search"
	webSearchTimeout       = 30 * time.Second
	webSearchMaxCount      = 50
	webSearchMaxQueryRunes = 128
	webSearchSummaryRunes  = 700
)

const webSearchDescription = "Search the live web. Returns ranked results with relevance and authority scores. " +
	"Write specific queries with entity names, full dates, and years for time-sensitive requests. " +
	"Disambiguate homonyms, use site: for authoritative sources, quoted phrases for exact titles, and -site: to exclude noise. " +
	"Read the returned evidence and answer the user; cite source URLs. " +
	"Plan the query first; refine at most twice if results are off-topic."

func webSearchParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Specific search query (max 128 characters). Include dates/years for time-sensitive requests. Use site:example.com when an authoritative source helps.",
			},
			"count": map[string]any{
				"type":        "integer",
				"description": "Number of results to return (1-50, default 10).",
			},
		},
		"required":             []string{"query"},
		"additionalProperties": false,
	}
}

// buildWebSearchTool creates the provider-agnostic web search tool.
func buildWebSearchTool(provider search.Provider, defaultCount int) Tool {
	return buildTool(webSearchName, webSearchDescription, webSearchParams(), func(a json.RawMessage) (string, error) {
		return webSearchExec(provider, defaultCount, a)
	})
}

func webSearchExec(provider search.Provider, defaultCount int, args json.RawMessage) (string, error) {
	var p struct {
		Query string `json:"query"`
		Count int    `json:"count"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}

	query := strings.TrimSpace(p.Query)
	if query == "" {
		return "Tool Error: 'query' is required and cannot be empty.", nil
	}
	if utf8.RuneCountInString(query) > webSearchMaxQueryRunes {
		return fmt.Sprintf(
			"Tool Error: query is %d characters; the search provider accepts at most %d. Use a shorter, more specific query.",
			utf8.RuneCountInString(query), webSearchMaxQueryRunes), nil
	}

	count := p.Count
	if count <= 0 {
		count = defaultCount
	}
	if count <= 0 {
		count = 10
	}
	if count > webSearchMaxCount {
		count = webSearchMaxCount
	}

	now := time.Now()
	executedQuery := enrichQuery(query, now)

	ctx, cancel := context.WithTimeout(context.Background(), webSearchTimeout)
	defer cancel()

	results, err := provider.Search(ctx, search.Request{
		Query:    executedQuery,
		Count:    count,
		Original: query,
	})
	if err != nil {
		return "Tool Error: " + err.Error(), nil
	}

	return formatWebSearchResults(results, query, executedQuery, now), nil
}

func formatWebSearchResults(results []search.Result, originalQuery, executedQuery string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Current date: %s (%s)\n", now.Format("2006-01-02"), now.Format("Monday"))
	if executedQuery != originalQuery {
		fmt.Fprintf(&b, "Original query: %s\n", originalQuery)
	}
	fmt.Fprintf(&b, "Executed query: %s\n", executedQuery)

	if len(results) == 0 {
		b.WriteString("\nNo web results found. Try a more specific query with entity names, a full date, or a site: filter.")
		return b.String()
	}

	b.WriteString("\n")
	for i, result := range results {
		title := strings.TrimSpace(result.Title)
		if title == "" {
			title = strings.TrimSpace(result.URL)
		}
		fmt.Fprintf(&b, "%d. %s\n", i+1, title)
		if url := strings.TrimSpace(result.URL); url != "" {
			fmt.Fprintf(&b, "   %s\n", url)
		}

		var meta []string
		if site := strings.TrimSpace(result.SiteName); site != "" {
			meta = append(meta, "site: "+site)
		}
		if !result.Published.IsZero() {
			meta = append(meta, "published: "+result.Published.Format("2006-01-02"))
		}
		if result.Score > 0 {
			meta = append(meta, fmt.Sprintf("score: %.1f/3", result.Score))
		}
		if result.Authority >= 0 {
			meta = append(meta, fmt.Sprintf("authority: %d/4", result.Authority))
		}
		if len(meta) > 0 {
			fmt.Fprintf(&b, "   %s\n", strings.Join(meta, " | "))
		}

		evidence := strings.TrimSpace(result.Summary)
		if evidence == "" {
			evidence = strings.TrimSpace(result.Snippet)
		}
		if evidence != "" {
			fmt.Fprintf(&b, "   %s\n", truncateRunes(evidence, webSearchSummaryRunes))
		}
		b.WriteString("\n")
	}

	b.WriteString("Answer from these results only. Cite source URLs. A score of 1.5 or higher and authority of 3 or higher indicate high-quality sources. " +
		"Prefer recent dates; flag stale ones and state uncertainty when sources conflict.")
	return strings.TrimRight(b.String(), "\n")
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

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit]) + "..."
}
