# Dependency And Result Contracts

This workspace keeps compatibility for existing callers while adding explicit
contracts for new integrations that need inspectable cancellation, terminal
state, final output, and dependency ownership behavior.

Use this note as the public migration guide for the Phase 4 dependency/result
repair batch. The legacy helpers remain available, but new code should choose
the explicit contracts below when ambiguity matters.

## Caller-Owned Cancellation

Blocking public calls that accept `context.Context` treat the caller as the
owner of cancellation and timeout behavior. When a repaired helper returns an
error, cancellation should be inspected with `errors.Is(err, context.Canceled)`
or `errors.Is(err, context.DeadlineExceeded)`.

For blocking message buffers, prefer:

- `messages.TypedBuffer.ReadContext(ctx)` for an error-returning read that
  reports caller cancellation with `ctx.Err()`.
- `messages.TypedBuffer.ReadBlockingContext(ctx)` only for legacy callers that
  still need the existing `(T, bool)` shape. Its `false` result is compatible,
  but it cannot identify cancellation separately from another closed `done`
  signal.

## Final Agent Output

For final text from `go-agent-loop`, prefer
`agentloop.ExecuteResult.FinalText()` over the legacy
`agentloop.ExecuteResult.Text()` helper.

`FinalText()` returns text plus explicit status and error metadata:

- non-empty success
- empty success
- no final assistant text
- caller cancellation or deadline
- terminal failure
- partial output with a terminal error

`Text()` remains available for compatible text-only callers that already treat
an empty string as acceptable.

## Stream Lifecycle

Existing streaming callers can continue to iterate with
`EventStream.HasNext()` and `EventStream.Response()`.

New callers that need terminal state should call `EventStream.Outcome()` after
iteration completes. It distinguishes:

- open stream
- clean drain
- caller close
- caller cancellation or deadline
- terminal failure
- partial output before terminal failure

Do not infer these lifecycle states from `HasNext() == false` in new code.

## Provider Runtime Ownership

Provider packages in `go-llm-gateway` own provider-specific protocol behavior.
Application composition owns generic runtime dependency policy such as live,
record, replay, and custom HTTP transport selection.

In this workspace, `agent-cli` builds the stateless provider HTTP runtime before
provider construction and passes it through
`agent-cli/internal/agent.ProviderBuildContext.HTTPClient`.
`WithProviderHTTPBaseTransport(...)` lets the composition layer inject the live
base transport; record mode wraps the selected base transport and replay mode
uses fixture replay.

Provider builders should consume the supplied client instead of silently
choosing application transport policy.

## Prompt Resolution Ownership

Prompt resolution is CLI-owned composition. The compatible
`Executor.LoadSystemPrompt(...)` helper returns the resolved prompt text.

Tests and composition code that need observability should use
`Executor.LoadSystemPromptWithDetails(...)`. It reports the prompt sources and
side effects consulted during resolution, including:

- literal prompt text
- prompt file reads
- default `AGENTS.md` creation and reads
- config-backed system-information settings
- runtime system-information collection
- skill metadata reads
- loop suffix appends

The CLI defaults remain compatible. A future library-grade prompt assembler can
inject these sources if consumers need pure prompt construction outside the CLI.

## Compatibility-Staged Work

The current repair batch intentionally avoids removing legacy declarations.
Remaining compatibility-sensitive work should be additive unless a future major
version explicitly breaks compatibility:

- add typed session send outcomes for the remaining `Session.Send(ctx, msg)
  bool` ambiguity, including cancellation, closed session, full outbound buffer,
  and terminal failure where the session implementation can observe them
- keep `ReadBlockingContext`, `Text`, and `HasNext` available until callers have
  migrated to the explicit contracts above
- split prompt assembly into pure injected loaders only if a future public
  library surface needs prompt composition outside the CLI

## Reviewer Commands

The Phase 4 contract repair evidence is credential-free. Run these commands
from the repository root:

```bash
go test ./agent-cli/internal/agent
go test ./go-agent-loop/pkg/agentloop
go test ./go-agent-loop/pkg/messages
make typecheck
make test
```
