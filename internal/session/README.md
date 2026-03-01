# Session Package

This package owns conversation session lifecycle, persistence, and active runtime orchestration.

## What a session contains

A session is represented by:

- `Metadata` (`metadata.json`): session id, title, timestamps, workspace, provider, model, client ownership, active status, temporary flag
- `Messages` (`messages.json`): full conversation history (system + user + assistant/tool messages)
- `Session` in-memory state: active LLM instance + agent context used while processing messages

Session data is stored under:

- `<session_dir>/<session_id>/metadata.json`
- `<session_dir>/<session_id>/messages.json`

## Core components

- `Manager`: creates, resumes, lists, loads, deletes, and manages ownership for sessions
- `Session`: owns one conversation state and persists message history after each turn
- `Runtime`: owns session workers and handles command dispatch
- `Store`: persistence contract used by session lifecycle operations
- `JSONLStore`: file-backed `Store` implementation for metadata and messages
- `MemoryStore`: in-memory `Store` implementation for temporary sessions

## Session Metadata and Ownership

Sessions store their ownership and routing state directly in metadata:

- `client_type`: the client type that owns this session (e.g., "tui", "telegram")
- `client_identity`: the identity of the owning client (e.g., "default", user ID)
- `active`: whether this is the currently active session for the client
- `temporary`: whether this is a non-persisted session (used for background jobs)

Active session semantics:
- One active session per `(client_type, client_identity)` pair
- When a new session is created/activated for a client, previous sessions are deactivated
- Active status is persisted in metadata

## Lifecycle

### Create

`Manager.Create(ctx, opts)` creates a new session with the given options:

1. Resolves provider/model
2. Validates options (non-temporary sessions require client binding)
3. For non-temporary sessions: deactivates any existing active session for the client
4. Creates new session id and directory (or MemoryStore for temporary sessions)
5. Saves metadata with ownership and active status
6. Builds system prompt from workspace context
7. Builds agent + tools
8. Writes initial messages containing the system prompt

CreateSessionOptions fields:
- `Bus`: event bus for publishing session events (required)
- `MaxAgentIterations`: limits the number of agent turns per request
- `Provider`, `Model`: LLM configuration (overrides defaults when set)
- `ClientType`, `ClientIdentity`: required for non-temporary sessions
- `Temporary`: creates in-memory-only session (no client binding required)

### Resume

`Manager.Resume(...)`:
1. Opens the existing session directory by id
2. Loads metadata + messages
3. Re-resolves provider/model for compatibility
4. Rebuilds current system prompt for the workspace
5. Normalizes messages so the first system message is updated (or inserted if missing)
6. Rebuilds agent + tools and returns a ready session

This means an old session can be resumed even after app restart, as long as session files exist.

## Session behavior during a turn

`Session.RunInteraction(...)` follows this flow:
1. Append user message to in-memory context
2. Run the agent with current full message history
3. Replace in-memory history with model output
4. Persist full updated message list to storage

Important details:
- Persistence happens after each completed turn
- A per-session mutex protects in-memory message state
- If the run fails, the error is returned and no successful assistant turn is persisted
- Temporary sessions persist only to memory (no disk I/O)

## Runtime responsibilities

`Runtime` manages session workers and command dispatch:
- Maintains a registry of active session workers
- Dispatches commands to specific session workers via `HandleCommandForSession()`
- Handles session creation and provider switching
- Publishes context token estimates and other events
- Supports both persisted and temporary sessions

## Provider and model semantics

- `SetModel` in `Session` supports **model change inside the same provider**
- If provider changes, a **new session** must be created and activated
- Resumed sessions re-resolve provider/model through current config for compatibility

## Persistence model and file contract

### `metadata.json`

Holds stable session attributes, timestamps, and ownership:
- `id`, `title`
- `created_at`, `updated_at`
- `workspace_dir`
- `provider`, `model`
- `client_type`, `client_identity` (owner)
- `active` (active status for the client)
- `temporary` (whether this is an in-memory-only session)

### `messages.json`

Stores full ordered conversation state used to resume:
- system prompt message first (normalized on resume)
- user/assistant/tool messages after that

Both metadata and messages are written atomically by writing a temp file and renaming.

## Public manager operations

- `Create(ctx, opts CreateSessionOptions)`: create fresh session (bound to client unless temporary)
- `Resume(ctx, id, opts CreateSessionOptions)`: load persisted session and rebuild runtime objects (uses Bus and MaxAgentIterations)
- `GetActiveSessionForClient(ctx, clientType, clientIdentity, opts CreateSessionOptions)`: get the active session for a client
- `SetSessionActive(sessionID)`: mark a session as active (deactivates others for same client)
- `List()`: list all sessions sorted by most recently updated
- `GetMetadata(ctx, id)`: fetch metadata only
- `LoadMessages(ctx, id)`: fetch persisted messages
- `Delete(id)`: remove full session directory

## Startup behavior (gateway mode)

In gateway mode:
- Router subscribes clients and resolves sessions via manager
- Each client gets their active session (resumed if exists, created if not)
- Runtime ensures a worker exists for each session
- Commands are dispatched to the appropriate session worker

## Notes and constraints

- Message history is persisted after each completed user turn
- `new_conversation` resets history to only the system prompt and persists it
- Changing provider creates a new session; same-provider model switch updates existing session metadata
- Temporary sessions are used for background jobs and do not persist to disk
- Events from temporary sessions are broadcast to all connected clients
