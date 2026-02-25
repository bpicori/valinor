# Gateway WebSocket Protocol

This document describes the WebSocket protocol exposed by the gateway so it can be tested manually (for example with Postman WebSocket requests).

## Endpoint

- Default WebSocket URL: `ws://127.0.0.1:7778/ws`
- Default health URL: `http://127.0.0.1:7778/health`
- Defaults come from `internal/config/config.go` and may be overridden by your `config.toml`.

## Envelope format

All frames are JSON envelopes:

```json
{
  "kind": "handshake | command | event | error",
  "type": "message_type",
  "request_id": "optional-correlation-id",
  "timestamp": 1735689600000,
  "payload": {},
  "error": "error message for kind=error"
}
```

Notes:
- `timestamp` is set by the server for outbound frames.
- `request_id` is optional on commands and is echoed back on error frames.
- Max incoming frame size is `1 MB`.

## Handshake (required first)

The first client frame **must** be a handshake of type `client_hello`.

### Client -> Gateway (`handshake/client_hello`)

```json
{
  "kind": "handshake",
  "type": "client_hello",
  "payload": {
    "protocol_version": 1,
    "client_type": "web",
    "client_identity": "postman-test-user"
  }
}
```

Allowed `client_type` values:
- `tui`
- `web`
- `telegram`
- `whatsapp`

### Gateway -> Client (`handshake/hello_ack`)

On success:

```json
{
  "kind": "handshake",
  "type": "hello_ack",
  "timestamp": 1735689600000,
  "payload": {
    "success": true,
    "session_key": "session:web:abcd1234",
    "provider": "openai",
    "model": "gpt-5",
    "context_tokens": 0,
    "tokens_estimated": true
  }
}
```

On failure, `success` is `false` and `payload.error` is populated.

## Supported commands (client -> gateway)

After handshake, send `kind: "command"` frames.

### `user_message`

```json
{
  "kind": "command",
  "type": "user_message",
  "request_id": "req-1",
  "payload": {
    "content": "Hello from Postman"
  }
}
```

### `new_conversation`

```json
{
  "kind": "command",
  "type": "new_conversation",
  "request_id": "req-2"
}
```

### `change_provider_model`

```json
{
  "kind": "command",
  "type": "change_provider_model",
  "request_id": "req-3",
  "payload": {
    "provider": "openai",
    "model": "gpt-5"
  }
}
```

### `create_new_session`

```json
{
  "kind": "command",
  "type": "create_new_session",
  "request_id": "req-4",
  "payload": {
    "provider": "openai",
    "model": "gpt-5"
  }
}
```

### `cancel_agent_run`

```json
{
  "kind": "command",
  "type": "cancel_agent_run",
  "request_id": "req-5"
}
```

## Supported events (gateway -> client)

Events are sent as `kind: "event"` and include one of these `type` values:

- `session_attached` payload: `{ "session_key", "provider", "model" }`
- `agent_content_delta` payload: `{ "content" }`
- `agent_content_full` payload: `{ "content" }`
- `agent_thinking` payload: `{ "step", "content" }`
- `agent_step_tokens` payload: `{ "step", "prompt_tokens", "completion_tokens", "total_tokens" }`
- `context_tokens` payload: `{ "prompt_tokens", "estimated" }`
- `agent_refusal` payload: `{ "content" }`
- `agent_tool_call_start` payload: `{ "tool_call_id", "tool_name", "tool_args" }`
- `agent_tool_call_done` payload: `{ "tool_call_id", "tool_name", "tool_result" }`
- `agent_done` payload: `{ "prompt_tokens", "completion_tokens", "total_tokens" }`
- `agent_error` payload: `{ "error" }`
- `agent_cancelled` payload: `{}`
- `new_conversation` (event type defined in router)
- `change_provider_model` (event type defined in router)

Example event:

```json
{
  "kind": "event",
  "type": "agent_content_delta",
  "timestamp": 1735689600123,
  "payload": {
    "content": "Hello"
  }
}
```

## Error frames

Validation or protocol failures are returned as `kind: "error"`:

```json
{
  "kind": "error",
  "type": "invalid_command",
  "request_id": "req-3",
  "timestamp": 1735689600999,
  "error": "change_provider_model requires 'provider' and 'model' fields"
}
```

Common `type` values:
- `parse_error`
- `protocol_error`
- `invalid_command`
- `command_failed`

## Quick Postman test flow

1. Start gateway mode: `go run ./cmd/valinor -mode=gateway`
2. In Postman, open a new WebSocket request to `ws://127.0.0.1:7778/ws`.
3. Send `client_hello`.
4. Confirm you receive `hello_ack` (and usually `session_attached`).
5. Send `user_message` command.
6. Observe streamed `agent_*` events and final `agent_done` (or `agent_error`).
