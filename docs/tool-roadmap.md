# Valinor Tool Roadmap

The tool surface follows one rule: a capability gets a tool only when it needs
something a skill cannot provide. Otherwise it belongs in `skills/`.

A tool is justified when it needs at least one of:

1. A secret the sandbox must not see (search, image generation).
2. To cross the sandbox boundary (host files, client delivery, scheduler DB).
3. Native non-text I/O the model consumes (images).
4. Atomicity or safety guarantees (path validation, read-before-edit).

PDF, Office, canvas, diagrams, browser automation, and research workflows are
skills: they are procedures over files, shell, and Python.

## Current tools

| Tool | Verdict | Work |
|---|---|---|
| `read_file` | Keep | Binary guard implemented: images and PDFs get an actionable hint. |
| `read_image` | Keep | Implemented: reads a workspace image and attaches it to the conversation. |
| `write_file` | Keep | Atomic temp+rename in P2. |
| `edit_file` | Keep | 8 MiB size guard in Wave 1. |
| `list_directory` | Keep | Bounded capture in Wave 1. |
| `grep` | Keep | Bounded capture in Wave 1. |
| `bash_exec` | Keep | Bounded output in Wave 1. Background execution is a TODO. |
| `python_exec` | Keep | Bounded output in Wave 1. |
| `send_image` | Keep | Inline image previews. |
| `send_file` | Keep | Implemented: sends a workspace file as a document attachment. |
| `ask_user` | Keep | Implemented: structured question with optional Telegram buttons. |
| `generate_image` | Keep | Register only when the provider is configured for image generation. |
| `cronjob` | Keep | Compact schema in P2. |
| `web_search` | Keep | Single search path when `[search]` is configured. |
| `web_fetch` | Keep | Only when the provider implements `search.Fetcher`. |
| `google_search` | Keep as fallback | Gemini only, when no `[search]` provider exists. |

## New capabilities

| Candidate | Verdict | Why |
|---|---|---|
| `job` | TODO | Needs completion injection before it is worth building. |
| `subagent` | TODO | Same mechanism: isolated context plus result injection. |
| MCP | Defer | Only if external tool servers become a requirement. |
| `glob`, `todo`, `calculator`, `memory_*` | Reject | `rg --files`, prompt instructions, `python_exec`, and `MEMORY.md` cover these. |

`http_request` is not on the roadmap.

## Priority order

### P0 — implemented (Wave 1 hygiene)

1. Register only tools the active provider can execute.
2. Contain tool panics so one bug does not kill the session.
3. Bound subprocess output at the process boundary.
4. Guard `edit_file` against very large files.

### P1 — implemented

5. **Vision input:** `read_image`, image attachments in
   OpenAI/Anthropic/Gemini messages, persisted image paths with runtime
   hydration, and inbound Telegram photos and image documents. Images are
   injected as a user message after tool results; the transcript stores paths,
   not base64. PDFs and Office files still belong to skills.
6. **File delivery:** `send_file` with an `AgentFile` event and Telegram
   `sendDocument`; TUI shows the workspace path. No bundled skills and no
   sandbox dependency changes: skills remain user-authored.
7. **`ask_user`:** non-blocking structured questions. The tool publishes an
   `AgentQuestion` event and ends the turn; the answer arrives as the next user
   message. Telegram renders one inline button per option and sends the choice
   as a normal user message. Excluded from scheduled agent runs. Blocking
   answers need the async mechanism below.
8. **Binary guard:** `read_file` detects images and PDFs and points at
   `read_image` or a document skill instead of dumping bytes.

### TODO — needs design

**Async jobs and subagents, including completion injection.** Do not build
`job` or `subagent` before this exists. Requirements:

- Spawn an isolated run or process, cancellable and owned by the parent session.
- Inject the result into the parent session without cancelling the active run.
  The normal user-message path cancels in-flight runs, so it is not a valid
  completion path.
- If the parent is idle, start a run with the result; if busy, queue it with
  clear ordering.
- Route to the owning client, deduplicate retries, handle cancellation, and
  clean up processes and sessions on shutdown.
- The scheduler's temporary-session and delivery model is the closest existing
  pattern; reuse its routing rather than adding a second delivery path.

This mechanism also unlocks blocking `ask_user` answers.

### P2 — later

- Tool metadata (`ReadOnly`, `ConcurrencySafe`) and parallel read-only calls.
- Atomic `write_file`; compact or split the `cronjob` schema.
- MCP and approvals only if the product needs them.

## Not tools

PDF, Office, canvas, diagrams, browser automation, and research workflows are
skills. `glob`/`find` is `rg --files`; planning is a prompt instruction;
arithmetic is `python_exec`; memory is `MEMORY.md` plus file tools.
