# Router Package

This package provides the internal API boundary between connected clients (TUI, Telegram, etc.) and the session/event core.

## Responsibility

- Validate client identity and protocol-level command shape
- Track connected clients (identity → consumer mapping)
- Route commands to the appropriate session via session manager
- Forward bus events to clients based on session ownership metadata

## Identity Model

A client identity is:
- `client_type` (e.g., `tui`, `telegram`)
- `client_identity` (stable ID for that client/user)

Combined identity key: `<client_type>:<client_identity>`

This key is used for:
- Client consumer lookup
- Session ownership resolution (via session metadata)

## Session Ownership

Sessions now store their owner directly in metadata:
- `client_type`: the type of client that owns the session
- `client_identity`: the identity of the owning client
- `active`: whether this is the currently active session for the client
- `temporary`: whether this is a non-persisted session (for background jobs)

The router uses session metadata for event routing:
- Events from owned sessions → sent only to the owning client
- Events from temporary sessions or with no owner → broadcast to all clients

## Subscribe Flow

When `SubscribeClient(...)` is called:
1. Validate identity (`client_type` allowed and `client_identity` not empty)
2. Check for existing active session via `manager.GetActiveSessionForClient()`
3. If no active session, create new session bound to this client via `manager.CreateForClient()`
4. Register the consumer for this identity
5. Send `session_attached` event to the client

## Command Routing

`HandleCommand(...)`:
1. Validates command identity
2. Gets active session for the client
3. Dispatches to the session worker via `runtime.HandleCommandForSession()`
4. For `create_new_session`, activates the new session via `manager.SetSessionActive()`

## Event Forwarding

Router subscribes to all bus events and routes them:
- Events with `SessionID=""` → broadcast to all clients
- Events from owned sessions → lookup metadata, send only to owner
- Events from temporary sessions → broadcast to all clients

Delivery is non-blocking; events are dropped if the client buffer is full.

## Notes

- Session creation/resume logic is delegated to `session.Manager`
- Identity-to-session persistence is handled via session metadata (no separate identity map)
- Handshake parsing lives in `internal/gateway`, which calls router APIs with typed identities/commands
