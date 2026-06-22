# Plan: Memory System Improvements (Hermes-Inspired)

## Goal

Upgrade Valinor's workspace memory from a simple file-injection model to a curated, bounded, agent-manageable system — adopting the best patterns from Hermes without copying its full complexity.

## Current State

| Piece | Location | Behavior |
|-------|----------|----------|
| `MEMORY.md` + daily logs | Workspace root | Injected into system prompt |
| `memory_log` / `memory_update` | [`internal/tools/memory.go`](../../internal/tools/memory.go) | Append daily / upsert section |
| Prompt injection | [`internal/context/context.go`](../../internal/context/context.go) | Up to 15k main + 5k daily chars, no usage indicator |
| End-of-turn nudge | [`internal/llm/llm.go`](../../internal/llm/llm.go) | Foreground memory prompt after 4+ user messages |

## What We're Adopting from Hermes

1. **`USER.md` separation** — user profile vs agent/project notes
2. **Unified `memory` tool** — `add` / `replace` / `remove` with substring matching
3. **Capacity feedback** — show usage % in injected prompt block
4. **Tighter bounds** — curated limits with consolidation prompts
5. **Duplicate prevention** — reject exact duplicate entries
6. **Light security scan** — block obvious injection patterns in memory writes

**Not in this plan:** session search (see [`session-search.md`](session-search.md)), background review fork, external memory plugins.

---

## Design

### File Layout

```
workspace/
├── MEMORY.md      # Agent notes: environment, project conventions, lessons learned
├── USER.md        # User profile: preferences, communication style, identity
└── memory/
    └── daily/
        └── YYYY-MM-DD.md   # Keep daily logs (unchanged)
```

### Character Limits (configurable)

| Store | Default limit | ~Tokens |
|-------|---------------|---------|
| `MEMORY.md` entries | 3,000 chars | ~750 |
| `USER.md` entries | 1,500 chars | ~375 |
| Daily lookback | 3,000 chars (7 days) | ~750 |

Lower than Valinor's current 20k injection — forces curation. Limits in `config.example.toml`.

### Entry Format

Entries separated by `§` delimiter (Hermes pattern), rendered in prompt as:

```text
══════════════════════════════════════════════
  MEMORY (agent notes) [67% — 2,010/3,000 chars]
══════════════════════════════════════════════
  Project uses Go 1.22, chi router, SQLite for metadata §
  Workspace sandbox runs in Podman, mounted at /workspace §
```

### Unified `memory` Tool

Replace `memory_log` and `memory_update` with a single tool:

```json
{
  "action": "add | replace | remove",
  "target": "memory | user | daily",
  "content": "...",
  "old_text": "..."   // required for replace/remove (substring match)
}
```

| Action | Behavior |
|--------|----------|
| `add` | Append entry to target store; error if over limit or duplicate |
| `replace` | Find entry by unique `old_text` substring, replace content |
| `remove` | Find entry by unique `old_text` substring, delete |

**Migration:** Keep `memory_log` / `memory_update` as deprecated aliases for one release, or remove immediately and update system prompt.

### Frozen Snapshot per Session

Capture memory content once at session start in [`internal/session/session.go`](../../internal/session/session.go). Do not re-read memory files mid-session. Changes persist to disk immediately via the tool but appear in the system prompt only on the next session/turn rebuild.

This matches Hermes's prefix-cache strategy and is important for Anthropic cost.

---

## Implementation Steps

### Phase 1: File model + loading

1. Add `USER.md` constant and `LoadUserProfile()` in [`internal/context/context.go`](../../internal/context/context.go)
2. Add `MemoryEntry` parser: split on `§`, count chars, compute usage %
3. Update `composeSystemPrompt()` to render MEMORY and USER blocks with capacity headers
4. Add config limits in [`internal/config/config.go`](../../internal/config/config.go):

```toml
[memory]
memory_char_limit = 3000
user_char_limit = 1500
daily_char_limit = 3000
daily_lookback_days = 7
```

5. Update [`internal/context/system_prompt.md`](../../internal/context/system_prompt.md) — document MEMORY vs USER vs daily, when to write each

### Phase 2: Unified memory tool

1. Create [`internal/tools/memory_v2.go`](../../internal/tools/memory_v2.go) (or refactor `memory.go`):
   - `parseEntries(content string) []string`
   - `addEntry(entries []string, new string, limit int) ([]string, error)`
   - `replaceEntry(entries []string, oldText, new string) ([]string, error)`
   - `removeEntry(entries []string, oldText string) ([]string, error)`
   - `serializeEntries(entries []string) string` — join with ` § `
2. Implement `memoryExec(rt, args)` with action dispatch
3. Add duplicate check on `add`: reject if `content` already exists as an entry
4. Add capacity check: return structured error with `current_entries` and `usage` when over limit (Hermes pattern — agent consolidates in same turn)
5. Remove or deprecate `memory_log` / `memory_update` from [`internal/tools/tools.go`](../../internal/tools/tools.go)
6. Update `memoryToolUsed()` in [`internal/llm/llm.go`](../../internal/llm/llm.go) to check for `memory` tool name

### Phase 3: Security scan

1. Add [`internal/context/memory_scan.go`](../../internal/context/memory_scan.go):
   - Reject entries matching injection patterns (e.g. "ignore previous instructions", `<system>`, invisible Unicode)
   - Reject entries over per-entry max (e.g. 500 chars single entry)
2. Call scanner in memory tool before write

### Phase 4: Tests

| Test file | Cases |
|-----------|-------|
| `internal/context/context_test.go` | Load MEMORY + USER, capacity header rendering, truncation |
| `internal/tools/memory_test.go` | add/replace/remove, substring matching, duplicate rejection, limit error |
| `internal/context/memory_scan_test.go` | Injection pattern blocking |

---

## Prompt Changes

Update [`internal/context/system_prompt.md`](../../internal/context/system_prompt.md):

```markdown
## Memory

You have a `memory` tool with three targets:
- **memory** — environment facts, project conventions, lessons learned
- **user** — user preferences, communication style, identity
- **daily** — brief append-only log entries for today's file

When memory is above 80% capacity (shown in the prompt header), consolidate
entries before adding new ones using `replace` to merge related facts.

Save proactively when you learn preferences, conventions, or corrections.
Do not save trivial or easily re-discovered facts.
```

---

## Migration Path for Existing Workspaces

On first load, if `MEMORY.md` uses `## Section` headers (current format):
- Keep sections as-is for v1 (backward compatible)
- Optionally add a one-time migration that converts `## Section` blocks to `§`-delimited entries

Daily logs (`memory/daily/`) remain unchanged.

---

## Files Changed

| File | Change |
|------|--------|
| `internal/context/context.go` | USER.md loading, capacity headers, entry parsing |
| `internal/context/memory_scan.go` | New security scanner |
| `internal/context/system_prompt.md` | Updated memory guidance |
| `internal/tools/memory.go` | Refactor to unified tool (or replace) |
| `internal/tools/tools.go` | Register `memory` tool, remove old tools |
| `internal/llm/llm.go` | Update `memoryToolUsed` check |
| `internal/config/config.go` | Memory limits config |
| `config.example.toml` | Document memory config |

## Dependencies

None. Builds on existing sandbox file I/O.

## Estimated Effort

~2–3 days.

## Success Criteria

- Agent can add/replace/remove memory entries with capacity feedback
- USER.md and MEMORY.md are separate and bounded
- Duplicate entries rejected
- Injection patterns blocked
- Existing daily log behavior preserved
- End-of-turn memory nudge still works (update to reference `memory` tool)