# Agent Instructions

Guidance for AI agents working in this repository.

## Testing Policy

**Do not write unit tests.** Do not create `*_test.go` files, test helpers, mocks for testing, or test fixtures unless the user explicitly asks for them.

When implementing features, verify correctness by building the project (`go build ./...`) and reasoning about the change. Do not add tests as part of routine implementation work.

## Go Coding Standards

### Core Design Principles

- Prioritize readability, simplicity, and maintainability.
- Design for change: isolate business logic and minimize framework lock-in.
- Emphasize clear boundaries and dependency inversion.

### Project Layout and Boundaries

- Use a consistent project layout:
  - `cmd/`: application entrypoints
  - `internal/`: core application logic (not exposed externally)
  - `pkg/`: shared utilities and packages
  - `api/`: gRPC/REST transport definitions and handlers
  - `configs/`: configuration schemas and loading
- Group code by feature when it improves clarity and cohesion.
- Keep domain logic independent from framework, transport, and persistence types.
- Validate and normalize input at system boundaries (transport, CLI, queue consumers).

### Function and File Organization

- Write short, focused functions with a single responsibility.
- Keep functions small enough that intent is clear without extra explanation.
- Order functions like a newspaper: place public and important methods first, helper details lower in the file.
- Document public functions with Go-style doc comments that start with the function name.
- Keep comments minimal; prefer self-explanatory code and comment only non-obvious intent.

### Dependencies, State, and Context

- Avoid global mutable state; prefer explicit dependency injection via constructors, parameters, and struct fields.
- Define small interfaces at consumption boundaries; prefer concrete types internally.
- Accept and propagate `context.Context` for operations that can block, cancel, or time out.
- Do not store `context.Context` in structs; pass it per call.

### Functional Style (Go-Friendly)

- Prefer pure functions for business rules (same input → same output, no hidden side effects).
- Use functional core / imperative shell: keep side effects at boundaries and transformations in the core.
- Favor immutable data flow; avoid mutating inputs unless in-place mutation is explicit in the contract.
- Compose behavior from small functions; use higher-order functions only when they improve clarity.
- Keep side effects explicit in names and signatures.
- Stay idiomatic Go: clarity over clever abstractions.

### Error Handling and Concurrency

- Return wrapped errors with actionable context (for example, `%w`); never swallow errors silently.
- Use sentinel/typed errors only when callers need stable branching behavior.
- Ensure goroutines have clear lifecycle ownership and cancellation paths.
- Protect shared mutable state with proper synchronization.

### Example

```go
// CreateUser creates a new user in storage.
func (s *Service) CreateUser(ctx context.Context, in CreateUserInput) (User, error) {
    // ...
}
```

## Static Analysis & Technical Debt Audit

When asked to run a technical debt audit, analyze **only** the code provided. Do not invent files, symbols, or behaviors that are not present in the provided code.

### Focus Areas (Priority Order)

1. Dead code: unused imports, variables, functions, unreachable branches.
2. Architectural violations: business logic in UI/infrastructure layers, domain depending on framework or I/O details.
3. Cognitive complexity: nesting depth > 3, methods longer than ~40 lines, boolean-heavy conditionals needing extraction.
4. Performance: redundant computations, O(n²) where O(n) is possible, repeated allocations in hot paths.
5. Readability: misleading names, inconsistent conventions, missing domain vocabulary.

### Severity Rubric

- **High:** bug risk, data-loss potential, or blocks scaling/onboarding.
- **Medium:** measurable maintenance cost; fix within 1–2 sprints.
- **Low:** style or minor inefficiency; fix opportunistically.

### Output Requirements

Return a numbered list. For each finding, use this exact template:

#### [#N] Short title — Severity: High | Medium | Low

- **Location:** `file:line` (or function/component name)
- **Issue:** One-sentence description of the problem.
- **Before:** minimal snippet showing the problem.
- **After:** minimal snippet showing the proposed fix.
- **Rationale:** Why this matters (max 2 sentences).

### Audit Constraints

- Do not report style-only issues as High.
- Do not suggest rewriting code that is already idiomatic for the framework in use.
- If no findings exist for a focus area, say "No issues found" for that area.
- Limit results to the top 15 findings, prioritized by severity then focus-area order.