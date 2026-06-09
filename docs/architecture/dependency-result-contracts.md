# Dependency And Result Contracts

This workspace keeps compatibility for existing callers while adding explicit
contracts for new integrations that need inspectable cancellation, terminal
state, final output, and dependency ownership behavior.

Use this note as the public migration guide for the Phase 4 dependency/result
repair batch. The legacy helpers remain available, but new code should choose
the explicit contracts below when ambiguity matters.

Reviewer-facing closure evidence for the broader dependency, result, context,
lifecycle, replay, prompt-resolution, and session-configuration map lives in
`docs/internal/phase-4-dependency-result-context-lifecycle-contract.md`.

## Caller-Owned Cancellation

Blocking public calls that accept `context.Context` treat the caller as the
owner of cancellation and timeout behavior. When a repaired helper returns an
error, cancellation should be inspected with `errors.Is(err, context.Canceled)`
or `errors.Is(err, context.DeadlineExceeded)`.

For blocking message buffers, prefer:

- `messages.TypedBuffer.ReadContext(ctx)` for an error-returning read that
  reports caller cancellation with `ctx.Err()`.
- `messages.TypedBuffer.WriteContext(ctx, value)` for a typed write outcome
  that distinguishes success, caller cancellation, timeout, and full buffers.
- `messages.TypedBuffer.ReadBlockingContext(ctx)` only for legacy callers that
  still need the existing `(T, bool)` shape. Its `false` result is compatible,
  but it cannot identify cancellation separately from another closed `done`
  signal.
- `messages.TypedBuffer.Write(ctx, value)` only for legacy callers that still
  need the existing `bool` shape. Its `false` result is compatible, but it
  collapses cancellation, timeout, and buffer-full outcomes.

For persistent sessions, existing `messages.Session.Send(ctx, msg) bool`
callers remain compatible. New callers that need lifecycle precision should use
`messages.SendSessionWithOutcome(ctx, session, msg)`. Sessions that implement
`messages.SessionSendOutcomeSender` can report success, caller cancellation,
timeout, full outbound buffers, closed sessions, and terminal failures. Bool-only
session implementations are adapted for cancellation and timeout, while other
legacy `false` results are reported as terminal failures because the precise
cause is not observable through the old interface.

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
- terminal source when a `MESSAGE.END` boundary was observed

`Text()` remains available for compatible text-only callers that already treat
an empty string as acceptable.

`FinalTextResult.TerminalSource` distinguishes provider-authored terminal
boundaries from loop-synthesized boundaries. Provider-authored boundaries are
reported as `messages.TerminalSourceProvider`, including legacy `MESSAGE.END`
values without explicit source metadata. Loop-synthesized boundaries are
reported as `messages.TerminalSourceLoopSynthesized` when the model runner
converts a non-streaming result into deltas or closes a provider stream that
ended without `MESSAGE.END`.

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
- provider-authored versus loop-synthesized `MESSAGE.END` boundaries

Do not infer these lifecycle states from `HasNext() == false` in new code.

`messages.MessageEndValue` carries optional `terminal_source` metadata. Use
`messages.MessageEndTerminalSource(value)` to normalize older empty-source
events as provider-authored. New loop-synthesized boundaries should be emitted
with `messages.NewSynthesizedMessageEndValue(...)`.

## Session Request Lifetime

Gateway-owned sessions use `models.SessionConfig` as the persistent session
shape and `ConnectSession(ctx, config)` for one connection attempt. The
`context.Context` passed to `ConnectSession` is caller-owned cancellation and
timeout state; it is not part of the persistent config.

Loop-managed session adapters use `inference.SessionRequest` to carry the same
persistent session shape into `SessionGatewayInferencer`. Configure it with
`inference.WithSessionRequest(...)` when the loop needs the full model,
modality, audio, tool, turn-detection, or provider-specific config. The older
`WithSessionModel`, `WithSessionVoice`, and `WithSessionInstructions` options
remain compatible wrappers for simple callers.

`SessionGatewayInferencer` copies the request on input, returns a copy from
`Request()`, and copies the config again for each `ConnectSession(ctx)` call.
Cancelling one connection attempt therefore does not mutate the persistent
request used by later attempts.

## Replay Lifecycle

For bidirectional session fixture replay, prefer
`testing.SessionReplayer.Outcome()` after `Done()` or after a send/close
failure. It reports:

- open replay
- successful completion
- divergent outbound replay
- incomplete replay where an expected capture event was omitted
- caller-owned replay context cancellation

`Outcome().Err` preserves the existing replay mismatch classifications, so
callers can continue to use `errors.Is(err, providers.ErrReplayMismatch)` or
`errors.Is(err, gateway.ErrReplayMismatch)`. `Outcome().Expected` and
`Outcome().Actual` expose mismatch detail for deterministic harnesses without
parsing log text. The older `SessionReplayer.Err()` helper remains available
for compatible error-only callers.

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

This is the compatibility-staged boundary for prompt composition in the current
repair batch. The CLI keeps ownership of prompt file reads, default `AGENTS.md`
creation, config loading, runtime system-information collection, skill metadata
discovery, and loop suffix assembly. Library callers that need pure prompt
construction should compose their prompt before calling the loop APIs, or use
`LoadSystemPromptWithDetails(...)` in tests to verify exactly which CLI-owned
sources and side effects were consulted.

The repaired dependency ownership decisions for these Phase 4 surfaces are:

| Dependency class | Ownership decision |
| --- | --- |
| Filesystem | CLI-owned for prompt files, `AGENTS.md`, config files, skills metadata, session storage, and capture files; provider replay fixtures are explicit caller-provided paths. |
| Environment | CLI-owned config loading may consult `AGENT_*` variables through the config storage layer; provider libraries receive resolved config and do not read CLI environment variables directly. |
| Process/runtime | CLI-owned system-information injection reads process runtime facts such as working directory, OS, architecture, and current time when not disabled. |
| Transport/network | Application composition owns HTTP transport, record/replay, and live network policy before constructing providers; provider packages own provider-specific protocol behavior. |
| Time | Caller-owned `context.Context` controls operation deadlines and cancellation; CLI system-information timestamps and capture event offsets remain CLI/testing side effects reported or isolated at their composition seams. |

## Compatibility-Staged Work

The current repair batch intentionally avoids removing legacy declarations.
Remaining compatibility-sensitive work should be additive unless a future major
version explicitly breaks compatibility:

- preserve the context/config lifetime split when extending loop-facing session
  adapters: `context.Context` should own one operation lifetime, while
  persistent session shape should remain explicit configuration such as
  `inference.SessionRequest`
- keep `ReadBlockingContext`, `Text`, and `HasNext` available until callers have
  migrated to the explicit contracts above
- keep prompt resolution as CLI-owned composition unless a future story
  introduces injected filesystem, config, system-information, skills, and clock
  dependencies as a separate library-grade prompt assembler
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
