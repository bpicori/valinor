// Package search provides web search and page fetching through a
// configurable provider.
package search

import (
	"context"
	"time"
)

// Request describes one web search.
type Request struct {
	// Query is the search query (at most 128 characters for most providers).
	Query string
	// Count is the requested number of results.
	Count int
	// Original is the user's question before query rewriting. Providers may
	// use it to improve intent understanding.
	Original string
}

// Result is one normalized search result.
type Result struct {
	Title     string
	URL       string
	Snippet   string
	Summary   string
	SiteName  string
	Published time.Time
	// Score is the provider relevance score; the scale is provider-specific.
	Score float64
	// Authority is the provider authority tier; -1 means unknown.
	Authority int
}

// Page is a fetched web page.
type Page struct {
	URL     string
	Title   string
	Content string // Markdown
}

// Provider performs web searches.
type Provider interface {
	Name() string
	Search(ctx context.Context, req Request) ([]Result, error)
}

// Fetcher is implemented by providers that can retrieve page content.
type Fetcher interface {
	Fetch(ctx context.Context, url string) (Page, error)
}
