# Session Package

This package owns conversation session lifecycle, persistence, and active runtime orchestration.

## What a session contains

A session is represented by:

- `Metadata` (`metadata.json`): session id, title, timestamps, workspace, provider, model.
- `Messages` (`messages.json`): full conversation history (system + user + assistant/tool messages).
- `Session` in-memory state: active LLM instance + agent context used while processing messages.

Session data is stored under:

- `<session_dir>/<session_id>/metadata.json`
- `<session_dir>/<session_id>/messages.json`

## Core components

- `Manager`: creates, resumes, lists, loads, and deletes sessions.
- `Session`: owns one conversation state and persists message history after each turn.
- `Runtime`: owns the currently active session and reacts to command events over the bus.
- `Store`: persistence contract used by session lifecycle operations.
- `JSONLStore`: file-backed `Store` implementation for metadata and messages.
- `IdentityMapStore`: helper store for identity-to-session mappings used by higher-level routing.

## Lifecycle

### Create

`Manager.Create(...)`:

1. Resolves provider/model.
2. Creates a new session id and directory.
3. Saves metadata.
4. Builds system prompt from workspace context.
5. Builds agent + tools.
6. Writes initial messages containing the system prompt.

### Resume

`Manager.Resume(...)`:

1. Opens the existing session directory by id.
2. Loads metadata + messages.
3. Re-resolves provider/model for backward compatibility.
4. Rebuilds current system prompt for the workspace.
5. Normalizes messages so the first system message is updated (or inserted if missing).
6. Rebuilds agent + tools and returns a ready session.

This means an old session can be resumed even after app restart, as long as session files exist.

## Session behavior during a turn

`Session.HandleUserMessage(...)` follows this flow:

1. Append user message to in-memory context.
2. Run the agent with current full message history.
3. Replace in-memory history with model output.
4. Persist full updated message list to storage.

Important details:

- Persistence happens after each completed turn.
- A per-session mutex protects in-memory message state.
- If the run fails, the error is returned and no successful assistant turn is persisted.

## Runtime responsibilities

`Runtime` is the active-session controller. It:

- Subscribes to bus commands (`user_message`, `new_conversation`, `change_provider_model`, `create_new_session`, `cancel_agent_run`, `request_provider_models`).
- Dispatches command handling to the active session.
- Cancels in-flight runs when a new run starts or when cancellation is requested.
- Publishes context token estimates for the active session.
- Switches active session when a new provider-specific session is created.
- Starts with no active session in gateway mode and attaches one on first client subscribe.

## Provider and model semantics

- `SwitchProviderModel` in `Session` supports **model change inside the same provider**.
- If provider changes, runtime creates a **new session** and switches active session.
- Resumed sessions re-resolve provider/model through current config for compatibility.

## Persistence model and file contract

### `metadata.json`

Holds stable session attributes and timestamps:

- `id`, `title`
- `created_at`, `updated_at`
- `workspace_dir`
- `provider`, `model`

### `messages.json`

Stores full ordered conversation state used to resume:

- system prompt message first (normalized on resume)
- user/assistant/tool messages after that

Both metadata and messages are written atomically by writing a temp file and renaming.

## Public manager operations

- `Create(ctx, opts)`: create fresh session and initial system message.
- `Resume(ctx, id, opts)`: load persisted session and rebuild runtime objects.
- `List()`: list all sessions sorted by most recently updated.
- `GetMetadata(ctx, id)`: fetch metadata only.
- `LoadMessages(ctx, id)`: fetch persisted messages.
- `Delete(id)`: remove full session directory.

## Startup behavior (gateway mode)

In gateway mode, runtime starts without an active session.
Session selection is client-driven:

- first client subscribe resolves a session (resume/create) in router
- runtime activates that session via `EnsureActiveSession(...)`
- subsequent commands for that identity use the active attached session

## Notes and constraints

- Message history is persisted after each completed user turn.
- `new_conversation` resets history to only the system prompt and persists it.
- Changing provider creates a new session; same-provider model switch updates existing session metadata.

