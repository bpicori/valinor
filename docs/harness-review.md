# Valinor Harness Review

Analysis of how Valinor manages tools, skills, context, and state, compared with
DeepSeek Harness, Claude Code 2.1.319, Codex CLI, and Pi (all as of November 2026).
Ends with the changes implemented in this pass and what was deliberately left out.

## Architecture at a glance

```
TUI / Telegram / Web
        │  WebSocket (gateway package)
        ▼
   Router ──► Session Runtime ──► sessionWorker (one run at a time)
                    │
                    ▼
              Session (agentcontext.Context)
                    │  messages + tools + system prompt
                    ▼
              llm.Run loop ──► Provider (OpenAI / Anthropic / Gemini)
                    │
                    ├── Compressor (summarize middle history)
                    └── tools.BuildTools ──► sandbox Runtime (Podman container)
                                             └─ workspace at /workspace
```

- **Tools** are a fixed list built in `internal/tools/tools.go`; every call goes
  through one process-global container (`internal/sandbox`).
- **Skills** are Markdown files under `<workspace>/skills/`; only `name` and
  `description` are injected into the system prompt, and the model loads a body
  by reading its path.
- **Context** is composed once per session (base prompt + date + `IDENTITY.md` +
  skill catalog + `MEMORY.md`) and compressed by the LLM loop when it grows.
- **State** is SQLite metadata plus a full `messages.json` rewrite per turn.
- **Scheduled jobs** run agent turns in temporary sessions; `no_agent` jobs run
  workspace scripts.

## Comparison with current harnesses

| Capability | Valinor (before) | DSH | Claude Code | Codex CLI | Pi |
|---|---|---|---|---|---|
| Tool-result capping | none | spill to file + path | ~30k chars inline, then file + preview | token-count truncation + metadata | 2000 lines / 50 KB + temp file |
| File reads | raw `cat` | offset/limit | line numbers, offset/limit, token cap | — | offset/limit, no gutter |
| Edits | exact replace, whole rewrite | read-before-edit guidance | exact + unique + read ledger | freeform `apply_patch` | exact multi-edit, unique, fuzzy fallback |
| Exec | per-call timeout | background jobs + terminals | process per call, backgrounding | session handles + `write_stdin` | no default timeout |
| Parallel tools | sequential | batched calls | yes | yes | yes, sequential preflight |
| Approvals | none | approval prompts | deny→ask→allow, 6 modes | policy + policy amendments | none built-in |
| Sandbox | root container, open network | file policy + approvals | Seatbelt/bwrap + proxy | Seatbelt/Landlock/bwrap, network off | containers |
| Skills | catalog + `read_file` | catalog + skill tool | 1% budget catalog, Skill tool, full frontmatter | `SKILL.md` + approval gate | `SKILL.md` + `/skill:name` |
| Subagents | none | subagent/fork + workflow | `Agent`, background, fork | hierarchical multi-agent | none built-in |
| Hooks | none | not exposed | ~30 events, exit-2 blocks | pre/post tool + compact | ~40 extension events |
| MCP | none | plugin manager | deferred tool search | stdio/HTTP, per-tool policy | built-in, code-mode exposure |
| Project docs | workspace only | `AGENTS.md` | `CLAUDE.md` hierarchy | `AGENTS.md` root→cwd | `AGENTS.md` + `SYSTEM.md` |
| Compaction | fixed 200k, LLM summary | not observable | auto + microcompaction | summary + user-turn budget | structured checkpoint |
| Caching | none | not observable | 4 breakpoints, tools in prefix | preserves prefix | 3 breakpoints + warmer |
| Transcript | full JSON rewrite | not observable | not public | JSONL + ordinals + fork | JSONL tree |
| Modes | none | plan mode | plan/auto/acceptEdits/bypass | sandbox × approval matrix | none (extension) |

Takeaways:

1. **Everyone bounds tool output; Valinor bounded nothing.** One command could
   fill the context, the client events, and the transcript at once.
2. **Approval is a protocol in the large harnesses.** Valinor relies on prose in
   the system prompt; that was accepted as out of scope here.
3. **Skills converged on three parts:** a budgeted description catalog, an
   invocation tool, and per-skill tool permissions. Valinor has only the first.
4. **Context is event-driven elsewhere.** Tagged reminders instead of mutating
   the system prompt; tool-result clearing before expensive summarization.
5. **Pi proves the lean direction works:** a ~1.5 KB prompt and four tools win
   through truncation, prefix stability, and exact-match edits, not tool count.

## Implemented in this pass (P0)

| Area | Change |
|---|---|
| Tool results | Capped at 24,000 characters at the execution point (head 16k + tail 8k + marker), so context, events, and `messages.json` shrink together. Blob hydration is capped too, so on-disk externalization can no longer inflate the prompt. |
| `read_file` | `offset`/`limit` (max 2000 lines, 20 KB per read) with a `[lines X-Y of N; next offset=…]` continuation hint. Raw lines, no gutter. |
| `edit_file` | Per-session read ledger (size + mtime): editing a file that was never read or changed since the read fails with a clear error. Successful edits return a compact diff instead of an opaque count. |
| Compression | Window is configurable (`[context] window_tokens`, default 200000). Triggers on `max(local estimate, provider-reported prompt tokens)`, so long sessions do not slide past the threshold. A provider context-length rejection forces one compression and retries the iteration. |
| Output limit | `[context] max_output_tokens` (default 8192) is now sent to every provider; Anthropic no longer silently defaults to 16384. |
| Retries | Transient provider errors (429/5xx/network) retry up to 3 times with jittered backoff; no retry once a delta has been emitted, so tool calls and partial answers are never duplicated. |
| Scheduled jobs | `no_agent` scripts run in the sandbox container; there is no host execution path. The `workdir` field was removed — it was unvalidated and never reached the tools. |
| Seeds | A stale `MEMORY.md` that still references removed memory tools logs an actionable warning. The bundled web-research skill was removed; `skills/` is now user-authored only. |
| Tool catalog | `todo_write`/`todo_read` and their in-memory store removed; 3+ step planning is now a one-line prompt instruction. Session README drift fixed. |

### Breaking changes

- `cronjob` no longer accepts `workdir`; stored jobs keep loading and the field
  is ignored.
- `no_agent` scripts must end in `.sh` and must live inside the workspace;
  there is no host runtime, and a `scripts_dir` outside the workspace is
  rejected at config load.
- `edit_file` now requires a prior `read_file` (or `write_file`) of the same path.

## Search providers

Web search is now provider-abstracted, mirroring the LLM provider layout:

- `internal/search` — `Provider` (Search) and optional `Fetcher` (Fetch) interfaces.
- `internal/search/factory` — selects the implementation from `[search] provider`.
- `internal/search/providers/deepseek` — DeepSeek Search & Fetch API.

`web_search` takes `query` + `count` and returns ranked results with score and
authority; `web_fetch` returns a page as Markdown. SearXNG was removed from the
image, config, and sandbox runtime: no background service, no readiness probe, no
port mapping, and the image build is a single stage. Adding another provider is
a new package plus one factory case and one config value.

## Deliberately not changed

- Container hardening (root user, open network, no resource limits) — accepted.
- Permission/approval layer — accepted.
- Log level filtering/redaction — accepted.
- `generate_image` writing to the host workspace — accepted.

## Next (not implemented)

- **Tool roadmap:** see [docs/tool-roadmap.md](tool-roadmap.md). Vision input is
  implemented; next are skill delivery (`send_file` plus bundled skills) and
  non-blocking `ask_user`. Async jobs and subagents wait on a
  completion-injection design.
- **Prompt caching:** deliberately removed. The primary provider (OpenRouter/DeepSeek)
  caches automatically, while Anthropic requires explicit `cache_control`
  breakpoints. Revisit only when Anthropic or Gemini becomes a primary provider.
- **Skills v2:** validate frontmatter, budget the catalog (~1% of context),
  add a `load_skill` tool, allow per-skill tool restrictions.
- **Tool registry:** metadata (read-only, concurrency-safe, risk) and parallel
  read-only calls.
- **Transcript:** append-only JSONL with resume/fork and session search.
- **Environment reminder:** inject cwd/model/date as a tagged reminder instead
  of patching the system prompt with string surgery.
- **Subagents / hooks / MCP / plan mode** — only if the product needs them.

## Verification

- `GOCACHE=$PWD/.tmp/gocache go build ./...` and `go vet ./...` pass.
- Manual smoke: page a large file, `cat` a multi-megabyte file and observe the
  cap, edit after an external modification, run a `no_agent` job and confirm it
  executes in the container.
- No unit tests were added, per `AGENTS.md`.
