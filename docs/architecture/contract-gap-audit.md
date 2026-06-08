# Contract Gap Audit

This document records contract gaps that reviewers and later implementation
lanes can cite directly. It starts with the hidden-coupling and
dependency-injection findings that Phase 2 needed to harden, and it now also
defines the Phase 4 exported API audit shape for public contract hardening in
`go-agent-loop/pkg` and `go-llm-gateway/pkg`.

Use [`dependencies.md`](./dependencies.md) for the intended dependency direction. Use this audit for the places where the codebase still relies on convenience coupling or constructor ownership that is broader than the intended architecture.

## Phase 4 Exported API Contract Audit Shape

Phase 4 audit findings must inspect exported public API surfaces under
`go-agent-loop/pkg` and `go-llm-gateway/pkg`, including `agentloop`,
`messages`, `gateway`, `inference`, `providers`, `models`, and `testing`.
Adjacent exported packages may be cited when they expose caller-visible
contract behavior relevant to the same gap.

This Phase 4 section is source material for later public API hardening lanes.
Creating or extending the audit does not close `P4-API-01`, `P4-API-02`,
`P4-API-03`, `P4-API-04`, `P4-API-05`, `P4-API-06`, or `P4-API-07` by itself.
Later planners must use the concrete findings and repair slices as inputs for
implementation work, tests, compatibility review, and release documentation.

### Phase 4 Checklist Rows

| Row | Contract area | Audit expectation |
| --- | --- | --- |
| `P4-API-01` | Context usage | Public blocking operations, buffer waits, provider calls, session connection, streaming, and I/O declare or document cancellation, timeout, and lifecycle behavior. |
| `P4-API-02` | Typed errors | Public errors and stream error events are caller-actionable through typed errors, sentinel support, structured fields, or documented error classes rather than string parsing. |
| `P4-API-03` | Result contracts | Public bool, nil, zero-value, channel-close, and loosely documented result signals are intentional, documented, and distinguish success, absence, cancellation, validation failure, and runtime failure. |
| `P4-API-04` | Capability discovery | Consumers can discover provider and gateway support for tools, streaming, sessions, audio, image input, video output, reasoning, prompt caching, and provider-specific configuration before sending a request. |
| `P4-API-05` | Stream semantics | Streaming APIs document event ordering, final events, close behavior, cancellation behavior, replay mismatch behavior, and error propagation. |
| `P4-API-06` | Dependency injection | Network clients, WebSocket dialers, clocks, stores, transports, endpoints, model names, timeouts, retry behavior, and other runtime dependencies are injectable or explicitly configured at public seams when callers need ownership. |
| `P4-API-07` | Go API hygiene | Exported names, doc comments, public struct exposure, nil/empty slice behavior, panic policy, and compatibility posture are clear enough for downstream package consumers. |

### Finding Fields

Each Phase 4 finding must use a stable identifier and include these fields:

- Identifier: a stable ID with an area prefix, for example `P4-CTX-01`,
  `P4-ERR-01`, `P4-RESULT-01`, `P4-CAP-01`, `P4-STREAM-01`, `P4-DI-01`, or
  `P4-HYGIENE-01`.
- Affected package: the public package path that exposes the contract.
- File path: the repository path containing the exported declaration or public
  behavior.
- Exported declaration: the exported type, interface, function, method, const,
  var, field, event value, or documented public behavior under review.
- Observable contract issue: the caller-visible ambiguity, failure mode, hidden
  dependency, lifecycle gap, or documentation gap.
- Mapped checklist rows: one or more of `P4-API-01` through `P4-API-07`.
- Severity: `must-fix contract defect`, `later polish`, or
  `release/documentation work`.
- Compatibility sensitivity: `additive`, `compatibility-sensitive`, or
  `documentation-only`.
- Recommended repair slice: the smallest implementation-ready change set that
  would repair or document the public contract.
- Verification notes: reviewer commands, expected unit or integration evidence,
  fixture updates, compatibility checks, or explicit statement that no
  reproducibility tooling was added for the audit finding.

Use this template for new findings:

```markdown
### P4-AREA-00: Short caller-visible problem statement

- Affected package: `module/pkg/name`
- File path: `path/to/file.go`
- Exported declaration: `Name`, `Type.Method`, `Interface`, or public event
- Observable contract issue:
  - what a package consumer can observe or cannot safely distinguish
- Mapped checklist rows: `P4-API-0X`, `P4-API-0Y`
- Severity: `must-fix contract defect` | `later polish` | `release/documentation work`
- Compatibility sensitivity: `additive` | `compatibility-sensitive` | `documentation-only`
- Recommended repair slice:
  - implementation-ready change that a later lane can own
- Verification notes:
  - command or test evidence expected from the later repair lane
```

### Classification Rules

Classify a finding as a `must-fix contract defect` when downstream callers
cannot reliably handle cancellation, validation, provider capability, stream
completion, typed failure classes, hidden runtime dependencies, or ambiguous
result states through the current public contract.

Classify a finding as `later polish` when the existing public contract is
usable but confusing, overly broad, awkwardly named, or likely to accumulate
maintenance risk without creating a direct caller-observable defect.

Classify a finding as `release/documentation work` when implementation behavior
is acceptable but the package docs, exported comments, migration notes, or
release guidance need to state the contract explicitly.

Mark changes as `compatibility-sensitive` when they alter exported signatures,
exported type shapes, event ordering, channel behavior, error text relied on by
existing tests or operators, fixture semantics, command completion timing, or
provider-visible validation timing. Mark changes as `additive` when they add new
typed errors, capability APIs, options, docs, or helper functions without
changing existing behavior. Mark changes as `documentation-only` when no
runtime or exported API behavior is expected to change.

### Phase 4 Context And Result Behavior Findings

These findings cover the `P4-API-01` and `P4-API-03` story slice. They name
public operations that block, wait on buffers, perform provider I/O, connect
sessions, or stream results where callers cannot yet distinguish cancellation,
timeout, absence, buffer pressure, and runtime failure as explicit public
contracts.

#### P4-CTX-01: execution result aggregation can block without a caller-visible timeout result

- Affected package: `github.com/portpowered/go-agent-loop/pkg/agentloop`
- File path: `go-agent-loop/pkg/agentloop/agent_loop.go`,
  `go-agent-loop/pkg/agentloop/execute_result.go`
- Exported declaration: `AgenticLoop.Execute`,
  `AgentLoop.Execute`, `AgenticLoop.ExecuteStreaming`,
  `AgentLoop.ExecuteStreaming`, `StreamingExecuteResult.Messages`
- Observable contract issue:
  - `Execute(ctx, input)` accepts a context and runs the hot loop under it, but
    the public contract does not state whether cancellation is returned as
    `ctx.Err()`, normalized into stream data, dropped as clean completion, or
    represented by a partial `ExecuteResult`.
  - `ExecuteStreaming(ctx, input)` returns a result immediately, and
    `StreamingExecuteResult.Messages()` later blocks on an internal `done`
    channel with no context parameter. A caller that stops consuming
    `EventStream`, closes the stream, or cancels the original context cannot
    tell from the exported API whether `Messages()` is guaranteed to unblock.
  - `Stream.HasNext()` returns `false` for both clean stream exhaustion and
    caller `Close()`. `Stream.Err()` exists, but the implementation does not
    currently populate it for context cancellation or loop failure, so callers
    cannot reliably distinguish "no more events" from cancellation or runtime
    failure.
- Mapped checklist rows: `P4-API-01`, `P4-API-03`, `P4-API-05`
- Severity: `must-fix contract defect`
- Compatibility sensitivity: `compatibility-sensitive`
- Recommended repair slice:
  - define the execution lifecycle contract for synchronous and streaming
    turns: when `ctx.Err()` is returned as an error, when partial deltas are
    preserved, when cancellation is emitted in-band, and when `Messages()` must
    unblock
  - add a context-aware result accessor such as `MessagesContext(ctx)` or make
    `Messages()` documented to unblock on `EventStream.Close()` and original
    context cancellation
  - populate `Stream.Err()` for caller cancellation, deadline exceeded, and
    loop/runtime failure while preserving clean exhaustion as `nil`
- Verification notes:
  - later repair lanes should add observable tests in
    `go-agent-loop/pkg/agentloop` for cancelled `Execute`, cancelled
    `ExecuteStreaming`, `EventStream.Close()` followed by `Messages()`, and
    `Stream.Err()` after cancellation and provider failure

#### P4-CTX-02: buffer waits collapse cancellation, empty buffer, and backpressure into `false`

- Affected package: `github.com/portpowered/go-agent-loop/pkg/messages`
- File path: `go-agent-loop/pkg/messages/buffers.go`,
  `go-agent-loop/pkg/messages/session.go`
- Exported declaration: `TypedBuffer.Write`, `TypedBuffer.Read`,
  `TypedBuffer.ReadBlocking`, `TypedBuffer.ReadBlockingContext`,
  `Session.Send`, `Session.Receive`
- Observable contract issue:
  - `TypedBuffer.Write(ctx, data)` returns `false` for context cancellation and
    a full buffer. `Session.Send(ctx, msg)` inherits that same bool-only result
    contract, so callers cannot tell whether a session write failed because the
    caller cancelled, the provider/session closed, or the outbound queue was
    full.
  - `TypedBuffer.Read()` returns `(zero, false)` for an empty buffer, while
    `ReadBlocking(done)` and `ReadBlockingContext(ctx)` return `(zero, false)`
    when the wait is stopped. The exported API does not give a typed reason for
    "no item yet" versus "the wait ended".
  - The only backpressure observability hook is `SetOnDrop`, which is tied to
    the full-buffer path for `Write`; consumers using `ReadBlockingContext` or
    `Session.Receive()` cannot observe a typed absence, cancellation, or closed
    session reason.
- Mapped checklist rows: `P4-API-01`, `P4-API-03`
- Severity: `must-fix contract defect`
- Compatibility sensitivity: `compatibility-sensitive`
- Recommended repair slice:
  - introduce an additive result type or error-returning variants for buffer
    operations, for example `WriteResult`/`ReadResult` or
    `WriteContext(...) error` and `ReadContext(...) (T, error)`, with stable
    reasons for success, empty, full, cancelled, and closed
  - keep existing bool methods as compatibility shims during migration, but
    document that `false` is intentionally lossy
  - thread the richer result through `Session.Send` and session relay wrappers
    so provider session code can distinguish cancellation from buffer pressure
- Verification notes:
  - later repair lanes should add buffer tests for full queue versus cancelled
    context and session tests proving `Send` exposes the richer reason without
    requiring string parsing

#### P4-CTX-03: gateway/provider calls document context plumbing but not cancellation and timeout outcomes

- Affected package: `github.com/portpowered/go-llm-gateway/pkg/gateway`,
  `github.com/portpowered/go-llm-gateway/pkg/providers`,
  `github.com/portpowered/go-llm-gateway/pkg/inference`
- File path: `go-llm-gateway/pkg/gateway/interfaces.go`,
  `go-llm-gateway/pkg/gateway/gateway.go`,
  `go-llm-gateway/pkg/providers/provider.go`,
  `go-llm-gateway/pkg/inference/main_inferencer.go`
- Exported declaration: `Gateway.Infer`, `Gateway.InferStream`,
  `DefaultGateway.Infer`, `DefaultGateway.InferStream`, `Provider.Infer`,
  `Provider.InferStream`, `GatewayInferencer.Infer`,
  `GatewayInferencer.InferStream`
- Observable contract issue:
  - these public methods accept `context.Context` and pass it down to provider
    implementations, but the exported comments do not specify whether
    cancellation/deadline failures are returned as `context.Canceled`,
    `context.DeadlineExceeded`, provider-specific errors, typed gateway errors,
    in-band stream events, or channel close without an error value.
  - `InferStream(ctx, req)` returns `(<-chan StreamMessage, error)`, but there
    is no public stream handle or final error result that can report a provider
    failure after the channel has been returned. This makes late cancellation,
    transport failure, and stream parser failure ambiguous unless every
    provider encodes them identically as stream messages.
  - `GatewayInferencer` bridges gateway results into loop results without
    documenting whether context cancellation is normalized, preserved, or
    converted during the bridge.
- Mapped checklist rows: `P4-API-01`, `P4-API-02`, `P4-API-03`,
  `P4-API-05`
- Severity: `must-fix contract defect`
- Compatibility sensitivity: `compatibility-sensitive`
- Recommended repair slice:
  - define one public cancellation/timeout contract shared by gateway and
    provider methods, including whether `errors.Is(err, context.Canceled)` and
    `errors.Is(err, context.DeadlineExceeded)` must work for direct calls
  - define the post-return streaming error path, either with a stream result
    handle, terminal error event taxonomy, or documented provider invariants
  - add bridge tests proving `GatewayInferencer` preserves the selected
    cancellation and timeout semantics into `messages.InferenceResult` and
    stream output
- Verification notes:
  - later repair lanes should add provider-neutral fake-provider tests under
    `go-llm-gateway/pkg/gateway` and bridge tests under
    `go-llm-gateway/pkg/inference` for direct cancellation, deadline exceeded,
    and late stream failure

#### P4-CTX-04: session connection and close contracts lack explicit lifecycle outcomes

- Affected package: `github.com/portpowered/go-agent-loop/pkg/messages`,
  `github.com/portpowered/go-llm-gateway/pkg/gateway`,
  `github.com/portpowered/go-llm-gateway/pkg/inference`,
  `github.com/portpowered/go-llm-gateway/pkg/testing`
- File path: `go-agent-loop/pkg/messages/session.go`,
  `go-llm-gateway/pkg/gateway/session_gateway.go`,
  `go-llm-gateway/pkg/inference/session_inferencer.go`,
  `go-llm-gateway/pkg/testing/session_inferencer.go`,
  `go-llm-gateway/pkg/testing/session_replay.go`,
  `go-llm-gateway/pkg/testing/session_record.go`
- Exported declaration: `SessionInferencer.ConnectSession`,
  `Session.Done`, `Session.Close`, `DefaultSessionGateway.ConnectSession`,
  `SessionGatewayInferencer.ConnectSession`,
  `RecordingSessionInferencer.ConnectSession`,
  `ReplaySessionInferencer.ConnectSession`, `SessionReplayer.Close`,
  `SessionReplayer.Err`, `SessionRecorder.Close`
- Observable contract issue:
  - `ConnectSession(ctx)` exposes cancellation for connection setup, but the
    public session contract does not state whether the same context owns the
    lifetime of `Receive()`, relay goroutines, replay delivery, or only the
    initial provider connection attempt.
  - `Session.Done()` only closes a channel; callers cannot tell whether the
    session ended because the caller closed it, the provider closed it,
    connection setup failed after partial initialization, replay diverged,
    context was cancelled, or a transport failed.
  - `Session.Close()` returns `error`, but the shared interface does not define
    idempotency outcome, whether close propagates provider close errors, or
    whether replay/capture wrappers must expose close-induced replay mismatch
    through `Err()`, returned errors, in-band events, or all of those.
- Mapped checklist rows: `P4-API-01`, `P4-API-03`, `P4-API-05`
- Severity: `must-fix contract defect`
- Compatibility sensitivity: `compatibility-sensitive`
- Recommended repair slice:
  - define a session lifecycle result model that separates connect failure,
    caller close, provider close, context cancellation, replay divergence, and
    transport failure
  - add an additive `Err()` or terminal status accessor to the shared
    `messages.Session` contract, or document why wrappers such as
    `SessionReplayer.Err()` are intentionally provider/testing-specific
  - align recorder and replayer relay contexts with the chosen lifecycle owner
    and document close idempotency across live, record, and replay sessions
- Verification notes:
  - later repair lanes should add shared session contract tests with fake
    sessions plus `go-llm-gateway/pkg/testing` tests for cancelled replay,
    replay divergence, caller close before expected outbound events, and
    recorder close behavior

#### P4-RESULT-01: text and fixture helpers use zero values for invalid or absent results

- Affected package: `github.com/portpowered/go-agent-loop/pkg/agentloop`,
  `github.com/portpowered/go-llm-gateway/pkg/gateway`
- File path: `go-agent-loop/pkg/agentloop/execute_result.go`,
  `go-llm-gateway/pkg/gateway/interaction_fixture.go`
- Exported declaration: `ExecuteResult.Text`,
  `InteractionFixtureReplayer.Fixture`, `InteractionFixtureReplayer.Replay`
- Observable contract issue:
  - `ExecuteResult.Text()` returns an empty string when there is no final
    assistant text, when the final assistant content is intentionally empty,
    when the result contains only reasoning/tool calls, or when execution
    ended before a final answer. Callers cannot distinguish absence from a
    valid empty response.
  - `InteractionFixtureReplayer.Fixture()` returns an empty
    `InteractionFixture{}` when cloning fails, collapsing impossible internal
    clone failure, absent fixture state, and a valid-but-empty zero value into
    the same public result shape.
  - `InteractionFixtureReplayer.Replay(ctx)` closes the channel without a
    terminal result when cloning fails or context is cancelled; a consumer
    cannot distinguish fixture preparation failure, cancellation, and successful
    replay of zero events by observing the channel alone.
- Mapped checklist rows: `P4-API-01`, `P4-API-03`
- Severity: `must-fix contract defect`
- Compatibility sensitivity: `additive`
- Recommended repair slice:
  - add explicit result helpers such as `FinalText() (string, bool)` or a
    richer execution final-message accessor while keeping `Text()` as a
    compatibility convenience
  - change fixture helpers additively by adding `FixtureResult()`
    `(InteractionFixture, error)` and a replay handle or terminal status that
    reports clone failure and context cancellation
  - document which zero values are valid public data and which indicate
    compatibility fallback behavior
- Verification notes:
  - later repair lanes should add observable tests for final empty assistant
    text versus absent final assistant text and for fixture clone/replay
    cancellation outcomes

#### P4-RESULT-02: interaction and tool-result validation errors are normalized inconsistently across sync, event, and fixture APIs

- Affected package: `github.com/portpowered/go-llm-gateway/pkg/gateway`
- File path: `go-llm-gateway/pkg/gateway/interaction_gateway.go`,
  `go-llm-gateway/pkg/gateway/interaction_fixture.go`,
  `go-llm-gateway/pkg/gateway/interaction_types.go`
- Exported declaration: `DefaultGateway.Interact`,
  `InteractionError`, `InteractionFixtureValidationError`,
  `ValidateInteractionFixture`, `DecodeInteractionFixture`
- Observable contract issue:
  - `Interact(ctx, req)` returns a channel successfully even when
    `validateInteractionToolResults(req)` later rejects the request; that
    validation failure appears as an in-band `InteractionEventError` with code
    `tool_result_validation_error`.
  - fixture validation APIs return `InteractionFixtureValidationError` directly
    as a Go error, while interaction runtime validation uses an event payload.
    Both surfaces are caller-visible validation contracts, but the audit found
    no shared rule for which validation failures are returned synchronously
    versus emitted asynchronously.
  - cancellation before provider execution emits a cancellation event and then
    an end event; cancellation during replay closes the channel without a
    terminal event. Consumers cannot apply one result-handling rule across live
    interaction and fixture replay.
- Mapped checklist rows: `P4-API-01`, `P4-API-02`, `P4-API-03`
- Severity: `must-fix contract defect`
- Compatibility sensitivity: `compatibility-sensitive`
- Recommended repair slice:
  - define a provider-neutral interaction result contract that says which
    validation failures return from the method call, which are terminal events,
    and which support `errors.As` against validation error types
  - align fixture replay cancellation and preparation failure with the live
    interaction terminal event contract, or add an explicit replay result/error
    handle when preserving channel-only replay behavior
  - add examples in package docs showing callers how to branch on validation,
    cancellation, timeout, provider error, and successful end
- Verification notes:
  - later repair lanes should extend `go-llm-gateway/pkg/gateway` tests to
    assert the selected sync-vs-event validation boundary and cancellation
    terminal behavior for both live `Interact` and fixture `Replay`

#### Context/Result Repair Slice Order

1. Repair `TypedBuffer` and `Session.Send` result ambiguity first because the
   bool-only contract sits beneath loop, session, record, and replay behavior.
2. Define gateway/provider cancellation and late-stream failure semantics before
   changing provider adapters, so direct gateway consumers and loop bridges get
   one shared rule.
3. Repair `AgenticLoop.ExecuteStreaming` completion and `Stream.Err()` after
   the lower-level stream/cancellation taxonomy exists.
4. Add explicit session terminal status after buffer and stream result reasons
   are available, then align live, recording, and replay sessions.
5. Add zero-value-safe convenience accessors for `ExecuteResult.Text()` and
   interaction fixture helpers as additive API polish once must-fix lifecycle
   defects are underway.

Reviewer commands for this audit-only story:

```bash
make typecheck
```

No reproducibility tooling was added for this slice; later implementation lanes
must add the tests named in each finding before closing any Phase 4 checklist
row.

## Intended Adapters vs Hidden Coupling

Intended adapter seams:

- `go-agent-loop/pkg/messages.Inferencer` implemented by `go-llm-gateway/pkg/inference.GatewayInferencer`
- `go-agent-loop/pkg/messages.SessionInferencer` implemented by `go-llm-gateway/pkg/inference.SessionGatewayInferencer`
- `go-llm-gateway/pkg/providers.Provider` and `pkg/providers.SessionProvider` selected by `agent-cli`

Hidden coupling:

- code in one module reaches into another module's testdata, file layout, or provider-specific helpers instead of depending only on the declared interface
- constructors create live transports or default executors internally instead of requiring the caller to own those dependencies
- application services build provider-specific runtime wiring directly, which makes the application layer the only place where richer behavior exists

The findings below are written so a reviewer can distinguish "this is the contract" from "this happens to work today because the current packages know too much about each other".

## Hidden Coupling Findings

### HC-01: `go-llm-gateway` test hygiene depends on `agent-cli` fixture layout

- Affected boundary: `go-llm-gateway/internal/sessionfixturevalidator` -> `agent-cli/test/integration/testdata`
- Evidence: `go-llm-gateway/internal/sessionfixturevalidator/committed_fixtures_test.go` hard-codes `../../../agent-cli/test/integration/testdata` as a committed fixture root.
- Observable impact:
  - a test-only change to `agent-cli` fixture locations can fail `go-llm-gateway` without any gateway contract change
  - the gateway module cannot validate its own fixture hygiene in isolation from the CLI worktree layout
  - reviewers can mistake cross-module fixture reuse for an intended dependency direction
- Why this is hidden coupling instead of an adapter:
  - the intended seam is shared session-capture shape under `go-llm-gateway/pkg/testing`, not a relative path from the gateway module into the CLI module
- Recommended Phase 2 hardening:
  - move shared committed session fixtures under a gateway-owned shared fixture package or shared workspace testdata root with an explicit ownership comment
  - keep `agent-cli` consuming those fixtures, rather than the gateway test suite walking into CLI-owned paths

### HC-02: `go-llm-gateway/pkg/models` is a naming facade over loop-owned message contracts

- Affected boundary: `go-llm-gateway/pkg/models` -> `go-agent-loop/pkg/messages`
- Evidence: `go-llm-gateway/pkg/models/message.go` aliases `messages.Message`, `messages.ToolDefinition`, `messages.TokenUsage`, and related types directly.
- Observable impact:
  - gateway consumers can import `pkg/models` and believe they are using an independent gateway contract, while the real compatibility anchor is still `go-agent-loop/pkg/messages`
  - contract review becomes harder because renames or field changes in loop-owned message types automatically leak into gateway-facing types
  - the gateway package naming suggests a separate vocabulary even though the two packages currently move in lockstep
- Why this is hidden coupling instead of an adapter:
  - an adapter would translate between loop and gateway vocabularies; the current code exposes the same types under two package paths
- Recommended Phase 2 hardening:
  - either document `pkg/models` as a deliberate alias layer with no independent compatibility promise, or define a truly gateway-owned model surface and add explicit translation at the boundary

### HC-03: session command behavior in `agent-cli` depends on provider-specific runtime helpers

- Affected boundary: `agent-cli/internal/services/session.go` -> `go-llm-gateway/pkg/providers/grok` and `pkg/providers/openai`
- Evidence:
  - `RunSession` and its helpers choose between Grok and OpenAI branches directly
  - replay and record flows construct `grok.NewDefaultWebSocketDialer`, `NewGrokSessionInferencerWithOptions`, `NewOpenAIRealtimeSessionInferencerWithOptions`, and an OpenAI-specific dialer adapter
- Observable impact:
  - richer session behavior exists only in the CLI composition layer, not behind one reusable session contract
  - adding a new realtime provider requires editing CLI service logic, not only registering a provider
  - provider-specific replay and recording semantics are harder to test as generic session behavior
- Why this is hidden coupling instead of an adapter:
  - the intended adapter seam is `messages.SessionInferencer`; branching on concrete provider packages inside the service is application wiring convenience above that seam
- Recommended Phase 2 hardening:
  - introduce a CLI-owned session runtime registry or factory interface that selects provider-specific session behavior behind one composition boundary
  - keep the provider-specific branch logic in one constructor-focused package instead of scattering it through service flow control
- Status after `phase-2-session-runtime-ownership-repair`:
  - resolved for the scoped Grok and OpenAI session record/replay paths
  - `agent-cli/internal/services/session_runtime.go` now owns the provider-specific session runtime planning behind one CLI composition seam before provider construction begins
  - reviewers should treat new provider-specific branching outside that seam as a regression unless it is explicitly documented as broader than the current Phase 2 scope

## Dependency-Injection Findings

### DI-01: `agentloop.New` creates a default tool executor when the caller omits one

- Affected boundary: `go-agent-loop/pkg/agentloop.New` constructor ownership
- Evidence: `go-agent-loop/pkg/agentloop/agent_loop.go` injects `&messages.DefaultToolExecutor{}` when `cfg.ToolExecutor` is nil.
- Observable impact:
  - loop construction succeeds even when the runtime has no real tool executor, and the failure shifts to the first tool call at runtime
  - callers cannot tell from construction-time behavior whether tool execution is intentionally disabled or accidentally missing
  - tests that forget to inject a tool executor may still build a loop that only fails later
- Why this is an injection gap:
  - the reusable loop owns fallback dependency creation instead of making the caller own the execution capability decision
- Recommended Phase 2 hardening:
  - decide explicitly whether tool execution is optional
  - if optional, model the absence as a declared no-tools mode
  - if required when tools are configured, fail construction when tools are present but no executor is injected
- Status after `phase-2-constructor-ownership-boundaries`:
  - completed
  - `agentloop.New(...)` now requires an explicit constructor-side capability decision when tool definitions are present
  - callers use `WithToolExecutor(...)` to enable execution or `WithToolExecutionDisabled()` for an intentional no-tools path

### DI-02: provider builders own HTTP transport creation and capture wiring

- Affected boundary: `agent-cli/internal/agent/provider_openai.go` and `provider_fal.go`
- Evidence:
  - builders construct `http.Client` values internally
  - record mode wraps `http.DefaultTransport` with `testing.NewRecordRoundTripper`
  - replay mode swaps in `testing.NewReplayRoundTripper`
- Observable impact:
  - transport policy, retry behavior, and recording concerns are coupled to provider construction instead of being injected as a separate dependency
  - future providers are likely to duplicate the same capture-wiring logic
  - callers cannot reuse one transport policy across providers without editing provider-specific builders
- Why this is an injection gap:
  - the factory API takes high-level config only, so transport ownership stays hidden inside each builder
- Recommended Phase 2 hardening:
  - inject transport or HTTP client policy through `ProviderBuildContext`, or add a narrower shared factory helper for record/replay transport ownership
  - keep provider builders responsible for provider options, not generic transport assembly
- Status after `phase-2-constructor-ownership-boundaries`:
  - completed for stateless provider runtime wiring in scope
  - `agent-cli/internal/agent.buildProviderHTTPRuntime(...)` now owns live, record, and replay HTTP client assembly once per execution path
  - provider builders consume the injected `ProviderBuildContext.HTTPClient` instead of choosing record/replay wiring or `http.DefaultTransport` internally

### DI-03: `Executor.loadSystemPrompt` mixes prompt resolution with filesystem and workspace side effects

- Affected boundary: `agent-cli/internal/agent.Executor`
- Evidence:
  - `loadSystemPrompt` reads arbitrary files, calls `workspace.EnsureAgentsMD`, reads `AGENTS.md`, loads config again for system info, and loads skills metadata
- Observable impact:
  - prompt resolution cannot be exercised as pure contract logic because it implicitly depends on filesystem state, workspace initialization, and configuration loading
  - a caller asking only for the resolved system prompt can still create files on disk
  - testing and future embedding become harder because one method owns multiple dependency types
- Why this is an injection gap:
  - the composition layer has not separated pure prompt assembly from IO-backed prompt source discovery
- Recommended Phase 2 hardening:
  - split prompt assembly into pure string composition plus injected loaders for filesystem-backed prompt sources, system info, and skills summaries
  - make `EnsureAgentsMD` an explicit composition step before prompt resolution instead of a side effect of reading the prompt

### DI-04: session service helpers load config and choose live dialers internally

- Affected boundary: `agent-cli/internal/services/session.go`
- Evidence:
  - `effectiveSessionProvider`, `resolveGrokSessionConfig`, and `resolveOpenAIRealtimeSessionConfig` call `config.NewDefaultConfigStorage(...).Load()` internally
  - live record paths create `grok.NewDefaultWebSocketDialer()` when no dialer is injected
- Observable impact:
  - session command behavior is harder to test as a pure service because config storage and network dialer ownership are embedded in helper functions
  - record/replay paths have different injection affordances: tests can pass a dialer, but production code silently falls back to a live default
  - cancellation and timeout policy for the session command is mixed with dependency creation rather than composed at one entrypoint
- Why this is an injection gap:
  - service-level runtime orchestration and dependency acquisition are still combined
- Recommended Phase 2 hardening:
  - introduce a session runtime dependency bundle for config loading, dialer selection, and provider-specific inferencer construction
  - keep `RunSession` responsible for command semantics, not for discovering defaults from disk and transport packages
- Status after `phase-2-session-runtime-ownership-repair`:
  - resolved for the scoped session runtime seam
  - `RunSession` now delegates session-mode config loading, dialer selection, and provider-specific runtime construction to `agent-cli/internal/services/session_runtime.go`
  - Grok and OpenAI session providers no longer create hidden live WebSocket dialers in the reviewed record/replay paths; missing owned dialers fail explicitly at the planner or provider session boundary

## Context Contract Findings

### CTX-01: session configuration is split between constructor-time options and call-time context

- Affected boundary: `go-agent-loop/pkg/messages.SessionInferencer` -> `go-llm-gateway/pkg/inference.SessionGatewayInferencer` -> `agent-cli/internal/services/session.go`
- Evidence:
  - `go-agent-loop/pkg/messages/session.go` defines `ConnectSession(ctx context.Context) (Session, error)` with no per-call request object
  - `go-llm-gateway/pkg/inference/session_inferencer.go` bakes only model, voice, and instructions into constructor options before converting them into `models.SessionConfig`
  - `agent-cli/internal/services/session.go` resolves provider config, replay mode, record mode, and prompt behavior outside the session inferencer contract
- Observable impact:
  - cancellation and deadline flow are explicit, but session-shaping inputs are not: callers can cancel a connection attempt, yet they cannot supply one complete per-session contract at call time
  - richer session settings already exist in `models.SessionConfig`, but loop-facing code cannot express them without constructing provider-specific adapters or CLI-only helper paths first
  - review of new session behavior becomes ambiguous because some runtime choices belong to context-aware command flow and others are hidden in constructor-time option state
- Why this is a context-contract gap:
  - the contract uses `context.Context` only for cancellation/lifetime, while the rest of the per-session execution context is spread across constructor options and CLI helper logic
- Recommended Phase 2 hardening:
  - define one explicit loop-facing session request/config contract that separates cancellation from session shape
  - keep provider translation in `go-llm-gateway`, but stop requiring the CLI to reconstruct missing per-session context from config and provider branches

### CTX-02: session replay and recording helpers drop back to `context.Background()` in relay paths

- Affected boundary: `go-llm-gateway/pkg/testing.SessionRecorder` and `go-llm-gateway/pkg/testing.SessionReplayer`
- Evidence:
  - `go-llm-gateway/pkg/testing/session_record.go` relays inbound session events with `relay.Write(context.Background(), msg)`
  - `go-llm-gateway/pkg/testing/session_replay.go` writes replayed outbound events with `r.outbound.Write(context.Background(), msg)`
- Observable impact:
  - replay/record infrastructure can continue draining or enqueueing messages even after the original caller context has been cancelled
  - timing-sensitive tests and future embedded runtimes cannot tell from the contract whether cancellation stops only provider IO or also the replay/record relay layer
  - the repo has inconsistent expectations for what "context cancellation" means once a session has been wrapped for testing or capture
- Why this is a context-contract gap:
  - helper layers that sit directly on the session contract do not preserve the same cancellation semantics as the live session path
- Recommended Phase 2 hardening:
  - document whether capture/replay buffers are intentionally best-effort after cancellation
  - if not, thread an explicit lifecycle context through relay goroutines so test and live session wrappers stop under the same cancellation contract
- Status after `phase-2-session-runtime-ownership-repair`:
  - narrowed substantially
  - `go-llm-gateway/pkg/testing.SessionRecorder` and `SessionReplayer` now accept an explicit relay lifecycle context, and the inferencer wrappers bind that lifecycle to `ConnectSession(ctx)` so relay writes stop when the owned caller/session context is cancelled
  - this repair resolves the remaining session-helper portion of the constructor-ownership validator scope and advances `P2-COB-04` plus `P2-GATE-01` by making cancellation ownership reviewer-visible at the same runtime seam as dialer ownership

## Typed-Error Findings

### ERR-01: the shared stream error contract is still mostly stringly typed

- Affected boundary: `go-agent-loop/pkg/messages.ErrorValue` consumed by loop, gateway, and CLI layers
- Evidence:
  - `go-agent-loop/pkg/messages/agent_messages.go` defines `ErrorValue` with optional `ErrorType`, `Code`, `Param`, and `EventID`
  - `go-agent-loop/pkg/participants/model_runner.go`, `go-agent-loop/pkg/participants/tool_runner.go`, and multiple provider stream adapters frequently emit `messages.NewErrorValue(err.Error())`, dropping category and structured details
- Observable impact:
  - callers can detect that an error occurred, but they usually cannot distinguish retryable provider failures, invalid user input, transport shutdown, replay divergence, or tool runtime errors from the shared contract alone
  - review of downstream compatibility is harder because error handling currently depends on matching free-form message text rather than named failure classes
  - Phase 2 changes to error wording could accidentally break callers or tests that only have the string payload to reason about
- Why this is a typed-error gap:
  - the contract has fields for structured classification, but most of the code path still collapses failures into a plain message string before crossing module boundaries
- Recommended Phase 2 hardening:
  - define a small set of caller-actionable error classes at the shared contract layer
  - require adapters to preserve provider/runtime classification when turning internal failures into `ERROR` stream messages

### ERR-02: session command failures mix transport, replay, and loop phases into wrapped text instead of one caller-actionable taxonomy

- Affected boundary: `agent-cli/internal/services/session.go` user-facing command errors
- Evidence:
  - `runLiveSessionRecord` and `runOpenAIRealtimeSessionRecord` return `errors.Join(...)` wrapped by `record session capture %s: %w`
  - replay paths return strings such as `replay session capture %s: %w` while runtime output paths also surface `session error: ...`
  - `wrapSessionPhaseError` only prefixes a phase string and does not attach a typed failure kind
- Observable impact:
  - a caller or future automation layer cannot reliably separate validation failure, replay divergence, provider rejection, session-close reason, and capture-flush failure without parsing message text
  - the CLI can explain roughly where a failure happened, but not in a way that downstream contract hardening or machine classification can depend on
  - some failures are emitted in-band as `ERROR` stream messages, while others escape as Go errors, with no unified documented mapping between the two
- Why this is a typed-error gap:
  - the command surface preserves human-readable context, but it does not yet expose stable error kinds that higher layers can branch on safely
- Recommended Phase 2 hardening:
  - define typed CLI/session failure categories for validation, provider-connect, replay-divergence, provider-runtime, and capture-persist paths
  - document which failures should remain in-band stream events versus returned command errors

## Stream And Session Lifecycle Findings

### LIFECYCLE-01: session-open, response completion, provider close, and command stop are separate boundaries with only partial shared ownership rules

- Affected boundary: `go-agent-loop/pkg/participants.ModelRunner` -> `agent-cli/internal/services/session.go`
- Evidence:
  - `go-agent-loop/pkg/participants/model_runner.go` sends `SESSION.UPDATE` after `SESSION.CREATED`, emits a synthetic `SESSION.CLOSE` with reason `provider_closed` when `session.Done()` fires before a close event, and treats audio barge-in as `RESPONSE.CANCEL`
  - `agent-cli/internal/services/session.go` stops the command on different events depending on mode: `MESSAGE.END`, `TEXT.END`, `SESSION.CLOSE`, timeout, injected `Done`, or explicit `sendSessionClose`
  - `shouldStopSessionLoop` changes command termination semantics based on `CloseAfterOpen` and `WaitForClose`
- Observable impact:
  - the codebase has at least four different lifecycle milestones that matter to callers, but the contract does not define which one owns "the session is complete" for each mode
  - replay and live modes can terminate on different boundaries even when they represent the same user-facing command
  - maintainers have to read both loop and CLI service code to know whether a provider close, message end, or caller close actually ends the session command
- Why this is a lifecycle-contract gap:
  - the shared session contract exposes `Done`, `Receive`, and `Close`, but ownership of command completion versus provider completion remains distributed across layers
- Recommended Phase 2 hardening:
  - document one canonical lifecycle state machine for session mode, including session-open, first-response-complete, client-requested-close, provider-close, and replay-complete
  - align loop and CLI stop conditions to that shared lifecycle rather than mode-specific helper heuristics

### LIFECYCLE-02: streaming inference completion rules differ between provider streams and loop-synthesized fallbacks

- Affected boundary: `go-agent-loop/pkg/participants.ModelRunner` and provider/gateway streaming adapters
- Evidence:
  - `go-agent-loop/pkg/participants/model_runner.go` treats `MESSAGE.END` or `ERROR` as streaming completion, but if the provider channel closes without `MESSAGE.END`, it emits a synthetic `MESSAGE.END`
  - the same runner falls back from `InferStream` to `Infer`, then synthesizes a full delta sequence for non-streaming results
- Observable impact:
  - downstream consumers see one normalized stream shape, but the contract does not make clear which completion boundaries are provider-authored versus loop-synthesized
  - interruption, replay, and compatibility review become harder because a missing upstream end event can still look like a clean stream to consumers
  - providers can differ in how faithfully they emit end-of-stream markers without that difference being visible in the public contract
- Why this is a lifecycle-contract gap:
  - the normalization is useful, but the observable contract does not currently distinguish "provider finished cleanly" from "loop closed the boundary for reconstruction"
- Recommended Phase 2 hardening:
  - document which stream boundaries may be synthesized by the loop and when that is acceptable
  - consider adding an explicit end/cancellation provenance signal if callers need to reason about provider completion versus normalization

## Naming And Doc-Comment Findings

### DOC-01: package names and exported aliases overstate contract independence in `go-llm-gateway/pkg/models`

- Affected boundary: `go-llm-gateway/pkg/models` -> downstream gateway consumers
- Evidence:
  - `go-llm-gateway/pkg/models/message.go` exports loop-owned message types under gateway-owned package naming
  - neither the package docs nor exported type comments make it explicit that this package is currently an alias facade over `go-agent-loop/pkg/messages`
- Observable impact:
  - maintainers and downstream consumers can read `pkg/models` as a stable gateway-owned vocabulary even though compatibility still follows loop-owned types
  - review discussions about renames or field moves have to rediscover whether the package is intended as a contract surface or only a convenience alias
- Why this is a naming/doc-comment gap:
  - the current package name suggests an independent data model, but the docs do not state the narrower intent of the package
- Recommended Phase 2 hardening:
  - add package-level documentation that states whether `pkg/models` is only a compatibility alias layer or a contract the gateway intends to own
  - if the alias role remains temporary, mark it clearly so new exported names do not accumulate under a misleading contract boundary

### DOC-02: exported `internal/*` composition helpers in `agent-cli` are not clearly documented as application wiring only

- Affected boundary: `agent-cli/internal/agent`, `agent-cli/internal/cli`, and `agent-cli/internal/services`
- Evidence:
  - exported types such as `Executor`, `ProviderFactory`, and command/router builders are used across sibling packages
  - the repository docs describe CLI features, but they do not currently name one canonical embedding boundary or state that these exported internal helpers are implementation seams rather than downstream APIs
- Observable impact:
  - future maintainers can treat exported `internal/*` types as compatibility-sensitive just because they are reused broadly inside the module
  - Phase 2 review of constructor, naming, or signature changes in CLI wiring requires re-deriving whether the change affects a user-facing contract at all
- Why this is a naming/doc-comment gap:
  - Go visibility alone does not communicate the intended stability of these helpers, and the current docs leave that contract boundary implicit
- Recommended Phase 2 hardening:
  - add package comments or development-doc guidance that distinguishes executable/config behavior from internal application wiring seams
  - prefer naming that reinforces composition ownership, such as runtime/factory terminology for internal orchestrators, when signatures are revised in Phase 2

## Compatibility Risk Findings

### COMPAT-01: changing shared message/session types will fan out across all three modules and recorded fixtures

- Affected boundary: `go-agent-loop/pkg/messages` consumed by `go-llm-gateway`, `agent-cli`, and replay/record fixtures
- Evidence:
  - the loop owns `Message`, `StreamMessage`, session interfaces, and error envelopes used across both other modules
  - gateway providers, CLI integration tests, and committed session fixtures all rely on those shapes
- Observable impact:
  - field renames, enum changes, or lifecycle event shape changes can break provider adapters, replay/record assertions, and CLI-visible output in one step
  - compatibility-sensitive work may look local to the loop package while actually invalidating cross-module fixtures and session transcripts
- Recommended Phase 2 hardening:
  - treat `pkg/messages` changes as cross-workspace contract changes and update fixture validation plus CLI replay coverage in the same iteration
  - prefer additive migrations or explicit translation shims before removing or repurposing existing fields/events

### COMPAT-02: session contract hardening can change CLI stop behavior and persisted capture semantics

- Affected boundary: `go-agent-loop/pkg/participants.ModelRunner` <-> `agent-cli/internal/services/session.go` <-> `go-llm-gateway/pkg/testing`
- Evidence:
  - current command completion depends on combinations of `MESSAGE.END`, `TEXT.END`, `SESSION.CLOSE`, timeout, and replay completion
  - replay/record helpers and committed `.session.json` captures encode the current event ordering and close behavior
- Observable impact:
  - clarifying lifecycle ownership in Phase 2 can alter when the CLI exits, when captures flush, and how replay fixtures are interpreted even if provider behavior stays the same
  - downstream scripts or tests that assume current command-exit timing may fail after apparently reasonable lifecycle cleanup
- Recommended Phase 2 hardening:
  - land lifecycle contract changes with explicit fixture updates and integration coverage for live, replay, and record paths
  - document user-visible behavior changes at the CLI/session level whenever stop conditions or close ordering change

### COMPAT-03: introducing typed error classes can break callers and tests that currently key off free-form text

- Affected boundary: `go-agent-loop/pkg/messages.ErrorValue`, provider adapters, and `agent-cli` session command errors
- Evidence:
  - many current paths still emit `err.Error()` strings or phase-prefixed wrapped errors
  - existing tests and operators can only infer meaning from message text because error type/code fields are sparsely populated
- Observable impact:
  - Phase 2 error normalization may require changing message text, adding codes, or moving some failures between returned Go errors and in-band stream errors
  - downstream automation that scrapes CLI stderr or recorded session events by substring may break unless the migration is staged carefully
- Recommended Phase 2 hardening:
  - introduce typed classifications additively first, while preserving legacy text long enough to migrate tests and operators
  - document the stable machine-readable fields that replace text matching before tightening or simplifying error wording

## Prioritized Phase 2 Execution Order

Bounded enabling steps that reduce accidental coupling before broader contract changes:

1. remove testdata path coupling by giving session fixtures one explicit owner
2. make constructor ownership explicit for tool execution and transport/dialer dependencies
   - status: completed for the Phase 2 constructor ownership boundaries step landed by `phase-2-constructor-ownership-boundaries`
3. centralize CLI session provider selection behind one factory boundary
4. clarify naming and package-doc intent for alias layers and exported internal wiring seams before adding new Phase 2 APIs to those areas

Compatibility-sensitive contract changes that should follow after the enabling steps:

5. define one loop-facing session request/lifecycle contract so context propagation and stop semantics no longer depend on CLI helper branches
6. introduce shared caller-actionable error categories before changing provider/session behavior more broadly
7. tighten shared message/session contracts only alongside fixture, replay, and CLI compatibility updates

This order keeps the early work focused on ownership and naming clarity first, then moves into the higher-risk session, error, and shared-message changes that can affect downstream behavior and recorded artifacts.
