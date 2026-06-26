---
name: web-research
description: Deep guidance for effective web_search queries, evidence synthesis, and avoiding stale or ambiguous results.
---

# Web Research

Load this skill when the user needs live external facts, news, schedules, prices, documentation, or anything not in the workspace.

## Before searching

1. Check **## Current Date** in the system prompt.
2. Decide if workspace files or memory already answer the question.
3. Write one planned query before calling `web_search`.

## Writing queries

- Include **entity + date + year** for time-sensitive requests.
- Disambiguate homonyms explicitly in the query text.
- Add `site:authority.com` when accuracy matters (official docs, sports fixtures, earnings).
- Use `categories: "news"` and/or `time_range: "day"` for breaking or very recent items.

## Retry budget

- At most **two refinements** after the first search.
- Refine by adding specificity, not by rephrasing vaguely.
- Do not launch parallel searches for the same intent.

## Synthesizing answers

- Answer only from returned snippets.
- Cite URLs for specific claims.
- Prefer snippets that match the current date; flag stale years.
- If evidence conflicts, state uncertainty — do not pick a side without support.
- Never answer "nothing found" when snippets contain relevant facts.

## Examples

| User intent | Weak query | Strong query |
|-------------|------------|--------------|
| Today's matches | `world cup today` | `FIFA World Cup 2026 matches June 26 2026 schedule site:espn.com` |
| Company earnings | `apple earnings` | `Apple Inc Q2 2026 earnings release site:apple.com` |
| Library docs | `enable json searxng` | `SearXNG settings.yml search formats json site:docs.searxng.org` |