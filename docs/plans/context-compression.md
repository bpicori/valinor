# Plan: Context Compression

## Goal

Prevent long sessions from hitting model context limits by automatically summarizing older conversation turns while preserving recent messages and the system prompt.

## Current State

- Full message history kept in [`internal/session/file_store.go`](../../internal/session/file_store.go) (`messages.json`)
- Token estimation exists in [`internal/tokens/estimate.go`](../../internal/tokens/estimate.go) — heuristic ~4 chars/token
- `ContextTokens` events published from [`internal/session/runtime.go`](../../internal/session/runtime.go) — UI shows estimate but no action taken
- No compression, truncation, or summarization anywhere in the agent loop

## Problem

As sessions grow, the agent will:
1. Hit provider context window limits (errors or silent truncation)
2. Degrade in quality (lost early context, noisy history)
3. Cost more per turn (larger prompts)

Hermes compresses at **50%** (preflight) and **85%** (emergency). Valinor should adopt a similar two-threshold approach.

---

## Design

### Compression Triggers

| Threshold | When | Action |
|-----------|------|--------|
| **Preflight (50%)** | Before each LLM call, if estimated tokens > 50% of model window | Summarize middle turns, keep system + first user + last N turns |
| **Emergency (85%)** | If still over 85% after preflight | Aggressive compression: keep system + last 10 turns only |

Model window sizes stored per model in config or a lookup table:

```go
var modelContextWindows = map[string]int{
    "gpt-4o":              128_000,
    "claude-3-5-sonnet-20241022": 200_000,
    "gemini-2.0-flash":    1_000_000,
    // fallback: 128_000
}
```

### What to Preserve (never compress)

1. System prompt message (first message with role `system`)
2. Last **K** conversation turns (configurable, default 20 messages / ~10 turns)
3. Any message containing unresolved tool calls in the preserved window

### What to Compress

Middle messages between the preserved head and tail. Replaced with a single synthetic user message:

```text
[Context Summary — N messages compressed]
<summary content>
```

### Summarization Strategy

**v1 (recommended):** LLM-based summarization using the same provider:

1. Extract messages to compress into a summarization prompt
2. Call provider with a one-shot "summarize this conversation" request (non-streaming, no tools)
3. Replace compressed range with summary message
4. Persist compressed history to `messages.json`

**v2 (optional):** Sliding window without LLM — truncate tool results in old messages to first 200 chars. Cheaper but lossier.

### Compression Record

Store metadata so the UI can show when compression happened:

```go
type CompressionRecord struct {
    At              time.Time `json:"at"`
    MessagesBefore  int       `json:"messages_before"`
    MessagesAfter   int       `json:"messages_after"`
    TokensBefore    int64     `json:"tokens_before"`
    TokensAfter     int64     `json:"tokens_after"`
}
```

Append to session metadata or a `compression_log.json` in the session dir.

---

## Implementation Steps

### 1. Model window lookup

**New file:** [`internal/llm/context_windows.go`](../../internal/llm/context_windows.go)

```go
func ContextWindowForModel(model string) int
```

Hardcoded defaults + config override in `config.toml`:

```toml
[context]
preserve_recent_messages = 20
preflight_threshold = 0.50
emergency_threshold = 0.85
```

### 2. Compression engine

**New file:** [`internal/llm/compressor.go`](../../internal/llm/compressor.go)

```go
type Compressor struct {
    provider Provider
    model    string
}

type CompressResult struct {
    Messages       []agentcontext.Message
    Compressed     bool
    Record         CompressionRecord
}

func (c *Compressor) MaybeCompress(
    ctx context.Context,
    messages []agentcontext.Message,
    windowSize int,
    threshold float64,
) (CompressResult, error)
```

**Algorithm:**

1. Estimate tokens via `tokens.EstimatePromptTokens(messages)`
2. If below threshold → return unchanged
3. Identify preserve regions:
   - `head`: system message + first user message (optional)
   - `tail`: last `preserve_recent_messages` messages
4. Extract `middle` = messages between head and tail
5. If `middle` is empty → return unchanged
6. Build summarization prompt from `middle`
7. Call `provider.Stream` or a new `provider.Complete` (non-streaming) with summarization system prompt
8. Assemble: `head + [summary message] + tail`
9. Re-estimate tokens; if still over emergency threshold, retry with smaller tail (keep last 10 only)

### 3. Integrate into agent loop

**File:** [`internal/llm/llm.go`](../../internal/llm/llm.go)

Before `l.provider.Stream(...)` on each iteration:

```go
compressResult, err := compressor.MaybeCompress(ctx, messages, windowSize, preflightThreshold)
if err != nil { /* log warning, continue with uncompressed */ }
messages = compressResult.Messages
if compressResult.Compressed {
    publishEvent(event.ContextCompressed, compressResult.Record)
}
```

### 4. New event type

**File:** [`internal/event/event.go`](../../internal/event/event.go)

```go
ContextCompressed EventType = "context_compressed"
```

Payload: `CompressionRecord`. TUI shows a brief "Context compressed" notice.

### 5. Persist after compression

**File:** [`internal/session/session.go`](../../internal/session/session.go)

After `RunInteraction` completes, if compression occurred during the run, save the compressed messages via existing `SaveMessages`.

### 6. Summarization prompt

**New file:** [`internal/llm/compression_prompt.md`](../../internal/llm/compression_prompt.md) (embedded)

```
Summarize the following conversation excerpt. Preserve:
- Key decisions and their rationale
- File paths and code changes mentioned
- User preferences and corrections
- Unresolved tasks or open questions
- Tool results that contain important data

Omit: greetings, repeated tool errors, verbose command output.
Write in third person, past tense. Be concise but complete.
Max 2000 tokens.
```

---

## Testing

| Test | File | Asserts |
|------|------|---------|
| Below threshold → no op | `compressor_test.go` | Messages unchanged |
| Above 50% → compresses middle | `compressor_test.go` | Head + summary + tail structure |
| Preserves system message | `compressor_test.go` | System message at index 0 |
| Preserves recent tail | `compressor_test.go` | Last N messages intact |
| Emergency fallback | `compressor_test.go` | Aggressive compression at 85% |
| Token estimate accuracy | `tokens/estimate_test.go` | Sanity check on sample conversations |

**Manual test:** Run a 30+ turn session, verify compression event fires and agent continues coherently.

---

## Risks

| Risk | Mitigation |
|------|------------|
| Summarization LLM call adds latency | Only triggers at 50%+; cache summary, don't re-summarize |
| Summary loses critical detail | Preserve tail generously (20 messages); instruct summary prompt carefully |
| Compression during tool loop breaks state | Never compress the current turn's in-flight tool calls (always in tail) |
| Cost of summarization call | One extra LLM call per compression event, not per turn |

---

## Files Changed

| File | Change |
|------|--------|
| `internal/llm/compressor.go` | New compression engine |
| `internal/llm/context_windows.go` | Model window lookup |
| `internal/llm/compression_prompt.md` | Summarization prompt |
| `internal/llm/llm.go` | Preflight compression hook |
| `internal/llm/compressor_test.go` | Unit tests |
| `internal/event/event.go` | `ContextCompressed` event |
| `internal/event/payloads.go` | `CompressionRecord` payload |
| `internal/config/config.go` | Compression config |
| `config.example.toml` | Document thresholds |
| `internal/tui/model.go` | Show compression notice (optional) |

---

## Estimated Effort

~2–3 days.

## Success Criteria

- Sessions with 50+ turns continue working without context errors
- Compression event visible in TUI
- Compressed history persisted and reloadable
- No compression of in-flight tool call turns