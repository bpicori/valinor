# Dreaming

Dreaming is Valinor's offline memory consolidation. While the instance is idle,
it reviews recent conversations and rewrites `MEMORY.md` into a cleaner, more
useful long-term memory.

## What it is not

- It does not modify session transcripts.
- It does not use agent tools, a sandbox container, or a temporary session.
- It does not run in a separate process or service.
- It does not train or fine-tune a model.
- It does not generate creative content, images, or simulations.

## Configuration

The only switch is on/off:

```toml
[dreaming]
enabled = true
```

Cadence, limits, and the provider/model are internal. Consolidation uses the
gateway's default provider and model. `enabled` is read at gateway startup.

## Scheduling

Dreaming is a goroutine inside the **gateway** process. There is no external
cron job, systemd timer, or CLI command.

- The goroutine wakes once a minute and checks whether a cycle is due.
- The first cycle runs shortly after dreaming is enabled, once no user turn is
  in flight and no transcript has changed for 15 minutes.
- Later cycles run at most once every 24 hours, measured from the last
  successful cycle.
- After a failure, a retry is scheduled 30 minutes later. After three
  consecutive failures the next attempt waits for the regular 24-hour interval.
- A cycle is only started when the instance is idle: no active agent run and no
  transcript activity for 15 minutes.

The schedule and retry timestamps are persisted in
`<instance>/dreaming/state.json`, so restarts do not cause a duplicate run and a
missed interval is picked up when the gateway comes back.

## Pipeline

```
gateway tick (1m)
  └─ dream goroutine ([dreaming].enabled)
       ├─ due?  (first run, 24h interval, or retry)
       ├─ idle? (no active run, 15m quiet)
       ├─ select changed sessions (mtime + digest hash; 14-day lookback)
       ├─ digest: user/assistant text only, truncated, hashed
       ├─ no changed sessions and memory within budget → no LLM call
       ├─ one provider call: current MEMORY.md + digest → complete new file
       ├─ parse <memory_file>, validate size and preserved headings
       ├─ MEMORY.md changed during call → discard; a live writer always wins
       └─ backup → atomic write → state marks → journal
```

The provider call has a 10-minute timeout. Because there is exactly one
executor goroutine and no CLI, there is no lock file. A live agent editing
`MEMORY.md` is handled by comparing the file before and after the call, and by
`edit_file`'s read-before-write signature check. Only one gateway process per
instance is supported.

## Prompt philosophy

The consolidation prompt is about the user's experience, not low-level data
hygiene:

- preserve who the user is, how they prefer to work, ongoing projects, goals,
  decisions, and commitments;
- merge duplicates and reconcile contradictions using the most recent correct
  information;
- organize facts into clear sections and remove stale notes;
- build on the current memory rather than discarding useful context;
- keep the result concrete and concise.

Credentials are not special-cased: if something is worth remembering, it may be
stored in `MEMORY.md`. Conversation excerpts are treated as context, not as
instructions to follow.

The prompt only keeps the machine contract that must be enforced: preserve
existing `# ` / `## ` headings, stay under the memory budget, and return the
complete file inside `<memory_file>...</memory_file>`.

## Context from previous dreams

The previous dream's result is the input: the current `MEMORY.md` is the
accumulated memory, and the prompt asks the model to build on it. Per-session
digest hashes in `state.json` ensure unchanged conversations are not resent.
Previous dream reports and raw transcripts are not replayed; only new or changed
sessions and the current memory are provided.

## State and retention

| Path | Purpose | Retention |
|---|---|---|
| `<instance>/dreaming/state.json` | watermarks, last attempt/success, retry time | single file |
| `<instance>/dreaming/backups/MEMORY-<UTC>.md` | pre-write snapshots | newest 10 |
| `<workspace>/memory/dreams/<UTC>.md` | aggregate dream report | newest 30 |
| `<workspace>/memory/dreams/latest.md` | copy of the latest report | — |
| `<workspace>/MEMORY.md` | consolidated memory | user-owned |

Reports contain only session IDs, sizes, headings, tokens, duration, backup
name, and errors. They do not contain transcript text or session titles.

## Validation and rollback

- The response must contain `<memory_file>…</memory_file>`; the closing tag
  detects truncation.
- Output must be non-empty, within the internal memory budget, and preserve
  every existing `# ` / `## ` heading.
- One corrective retry is allowed for contract violations.
- `MEMORY.md` is read before the call and again before writing. Any change in
  between discards the dream output.
- An existing `MEMORY.md` is backed up before every write.
- If a change must be rolled back, stop the gateway and copy a file from
  `<instance>/dreaming/backups/` over `MEMORY.md`.

## Privacy and cost

- Opt-in. Enabling it sends transcript excerpts to the configured provider.
- Transcript excerpts are limited to user/assistant text; tool results, images,
  and system prompts are excluded. The current `MEMORY.md` is sent as the input
  to rewrite.
- Cost is bounded internally: incremental session hashes, at most 20 sessions,
  digest/message/session caps, a 4096-token response cap, one corrective retry,
  and failure backoff.
- `MEMORY.md` is instance-global, as it was before dreaming. Use separate
  instances if different people should not share memory.

## Live sessions

`PrepareRun` reloads the `## LoadedMemory` section from disk on every turn. A
dream's consolidation is therefore visible to long-lived sessions (Telegram,
TUI) at their next message without a restart. This also fixes the previous stale
snapshot after an agent edited `MEMORY.md` mid-session.
