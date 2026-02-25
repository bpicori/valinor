# Router Package

This package is the boundary between connected clients (web, tui, telegram, whatsapp) and the session/event core.

It is responsible for:

- validating client identity and protocol-level command shape
- attaching clients to sessions
- reconnecting clients to previous sessions when possible
- forwarding client commands to the bus
- forwarding bus events back to connected clients

## Identity model

A client identity is:

- `client_type` (for example `web`, `tui`)
- `client_identity` (stable id for that client/user)

Router combines them into an identity key:

- `<client_type>:<client_identity>`

This key is the lookup key for session bindings and persistence mapping.

## Reconnect/resume flow

When `SubscribeClient(...)` is called:

1. Validate identity (`client_type` allowed and `client_identity` not empty).
2. Check in-memory registry for an existing binding.
   - If found: ensure runtime is active on that session, replace old consumer, and emit `session_attached`.
3. If not found, check persisted identity mapping (`IdentityMapStore`) for `identity_key -> session_id`.
4. If mapping exists, try `session.Manager.Resume(mapped_session_id, ...)`.
   - If resume succeeds: bind client to resumed session and emit `session_attached`.
   - If resume fails: remove stale mapping and continue to create a new session.
5. If no mapping (or mapping is stale), create a new session via `session.Manager.Create(...)`.
6. Attach resolved session as runtime active session (`Runtime.EnsureActiveSession(...)`).
7. Register binding in memory and persist/update mapping for future reconnects.

Expected result:

- same identity reconnects to same previous session whenever session files still exist
- stale mapping auto-recovers by creating a new session
- different identities are isolated by key

## In-memory vs persisted state

- `SessionRegistry` (in-memory): fast map of current process bindings.
- `IdentityMapStore` (persisted): survives process restart and enables resume on next connection.
- `clientConsumers` (in-memory): active connection endpoints used to push events.

## Command routing

`HandleCommand(...)`:

1. Validates command identity.
2. Ensures identity has an active session binding.
3. Converts command to bus event.
4. Publishes event to runtime/session handlers.

If no binding exists, command is rejected.

## Event forwarding

Router subscribes to bus events (`agent_*`, `context_tokens`, provider model responses, etc.) and forwards each event to all connected consumers.

Delivery is non-blocking:

- if a consumer queue is full, event is dropped for that consumer
- router logs the drop instead of blocking all clients

## Notes

- Router does not own session persistence internals; it coordinates with `internal/session`.
- Session creation/resume logic is centralized in `getOrCreateSessionForIdentity(...)`.
- Handshake parsing/validation lives in `internal/gateway`, then gateway calls router APIs with typed identities/commands.
- Gateway no longer creates a bootstrap session at startup; first client subscribe drives session activation.
