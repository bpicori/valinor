# Plan: Parallel Tool Execution

## Goal

Run independent read-only tool calls concurrently within a single agent turn, reducing latency when the model emits multiple `read_file` / `grep` / `list_directory` calls at once.

## Current State

- Tool calls execute sequentially in [`internal/llm/llm.go`](../../internal/llm/llm.go) (lines 185–215).
- The Podman sandbox already supports up to 10 concurrent ops via a semaphore in [`internal/sandbox/runtime.go`](../../internal/sandbox/runtime.go) (`defaultMaxConcurrentOps = 10`).

## Tool Classification

| Tool | `ParallelSafe` | Reason |
|------|----------------|--------|
| `read_file` | `true` | Read-only |
| `list_directory` | `true` | Read-only |
| `grep` | `true` | Read-only |
| `google_search` | `true` | External read |
| `todo_read` | `true` | Read-only |
| `cron_list` | `true` | Read-only |
| `cron_history` | `true` | Read-only |
| `bash_exec` | `false` | Shared container shell |
| `python_exec` | `false` | Shared container shell |
| `write_file` | `false` | File write race |
| `edit_file` | `false` | File write race (always serial) |
| `memory_log` | `false` | Append to shared daily file |
| `memory_update` | `false` | Write to MEMORY.md |
| `cron_add` | `false` | Mutates job store |
| `cron_remove` | `false` | Mutates job store |
| `todo_write` | `false` | Mutates todo store |
| `send_image` | `false` | Client-side delivery; keep serial for simplicity |

**Note:** `edit_file` is always serial — no per-path parallel writes in v1.

## Implementation Steps

### 1. Add `ParallelSafe` to `Tool` struct

**File:** [`internal/tools/tools.go`](../../internal/tools/tools.go)

```go
type Tool struct {
    Name         string
    Description  string
    Parameters   json.RawMessage
    ParallelSafe bool
    Exec         func(json.RawMessage) (string, error)
    runContext   *RunContext
}
```

Update `buildTool` to accept `parallelSafe bool`. Mark each tool in `BuildTools()` accordingly.

### 2. Add lookup helper

**File:** [`internal/tools/tools.go`](../../internal/tools/tools.go)

```go
func ParallelSafeByName(tools []Tool) map[string]bool
```

Returns a map from tool name → parallel-safe flag for the LLM loop.

### 3. Extract tool execution into a batch executor

**New file:** [`internal/llm/tool_executor.go`](../../internal/llm/tool_executor.go)

```go
type toolCallResult struct {
    index      int
    toolCallID string
    toolName   string
    result     string
}

func executeToolBatch(
    ctx context.Context,
    tools []tools.Tool,
    calls []ToolCall,          // from provider result
    parallelSafe map[string]bool,
    onStart func(tc ToolCall),
    onDone  func(tc ToolCall, result string),
) ([]agentcontext.Message, error)
```

**Algorithm:**

1. Build `[]toolCallResult` slice sized to `len(calls)`, keyed by original index.
2. Partition calls into two groups while preserving index:
   - **Parallel group:** `parallelSafe[name] == true`
   - **Serial group:** everything else
3. Run parallel group with `golang.org/x/sync/errgroup`:
   - Each goroutine checks `ctx.Err()` before starting
   - Calls existing `executeTool` logic
   - Writes result into `results[index]`
   - Invokes `onStart` / `onDone` callbacks (events may arrive out of order — acceptable)
4. Run serial group in original call order, sequentially.
5. Append `agentcontext.Message{Role: RoleTool, ...}` in **original index order** (critical for provider APIs).

### 4. Replace sequential loop in `llm.go`

**File:** [`internal/llm/llm.go`](../../internal/llm/llm.go)

Replace lines 185–215 with a call to `executeToolBatch`, passing the existing `publishEvent` closures for `AgentToolCallStart` / `AgentToolCallDone` / `AgentThinking`.

### 5. Add dependency

```bash
go get golang.org/x/sync/errgroup
```

(Or use `sync.WaitGroup` + error channel if avoiding new deps — errgroup is cleaner.)

### 6. Config (optional, v1 can skip)

Add to [`config.example.toml`](../../config.example.toml):

```toml
max_parallel_tools = 10  # align with sandbox sem
```

Cap parallel goroutines with a semaphore if config is added.

## Testing

**New file:** [`internal/llm/tool_executor_test.go`](../../internal/llm/tool_executor_test.go)

| Test | Asserts |
|------|---------|
| `TestExecuteToolBatch_AllParallel` | 3 parallel-safe tools run concurrently (use timing or call counter) |
| `TestExecuteToolBatch_MixedBatch` | Serial tools run after parallel group; order preserved in output messages |
| `TestExecuteToolBatch_PreservesOrder` | Results appended in original tool-call index order regardless of completion order |
| `TestExecuteToolBatch_Cancellation` | `ctx.Cancel` stops in-flight parallel tools |
| `TestExecuteToolBatch_UnknownTool` | Returns `"error: tool not found"` for missing tool |

**Integration:** Manually trigger a turn where the model calls multiple `read_file` on different paths; verify TUI shows all tool-done events and correct results.

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Tool result order mismatch | Always reassemble by original index before appending messages |
| Sandbox overload | Existing sem (10) already limits concurrent podman exec |
| Event ordering in TUI | Document that parallel tool events may interleave; optional v2: buffer events until batch completes |
| Race on `RunContext` | `RunContext` already has `sync.RWMutex`; tools reading session ID are safe |

## Out of Scope (v1)

- Per-path write locking for `write_file` / `edit_file` on different files
- Parallel `bash_exec` with separate containers
- Cross-turn tool parallelism

## Files Changed

| File | Change |
|------|--------|
| `internal/tools/tools.go` | Add `ParallelSafe` field + mark tools |
| `internal/llm/tool_executor.go` | New batch executor |
| `internal/llm/llm.go` | Use batch executor |
| `internal/llm/tool_executor_test.go` | Unit tests |
| `go.mod` | Add `golang.org/x/sync` if needed |

## Estimated Effort

~1 day. Low risk, high payoff for exploration-heavy turns.