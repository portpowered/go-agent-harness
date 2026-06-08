# Phase 4 Dependency Result Contract Repair Evidence

This note is the reviewer-facing closure map for
`phase-4-dependency-result-contract-repair`. It depends on the reconciled audit
from `phase-4-api-audit-reconciliation`; that artifact is not present in this
checkout. Rows whose exact reconciled-audit source cannot be verified from this
worktree are marked `uncertain` instead of closed from PRD planning prose.

Evidence must come from current public declarations, observable runtime
behavior, tests, CLI behavior, examples, or explicit compatibility staging. A
row is not closed by this document unless the decision below says `pass`.

## Current Public Evidence

- `docs/architecture/dependency-result-contracts.md` is the public migration
  and compatibility-staging guide for the Phase 4 cancellation, result,
  lifecycle, provider-runtime, and prompt-resolution contracts.
- `go-agent-loop/pkg/agentloop.AgenticLoop` exposes context-first blocking
  entrypoints for `Execute`, `ExecuteStreaming`, `Run`, `Send`,
  `SendInteractionEvents`, `Pause`, `GetState`, `SetInputs`, and `SetOutputs`.
- `go-agent-loop/pkg/messages.Inferencer`, `ToolExecutor`, and
  `SessionInferencer` use `context.Context` on provider, tool, and session
  connection calls.
- `go-agent-loop/pkg/messages.Session.Send` returns `bool`; the current public
  declaration says `false` may mean context cancellation or a full outbound
  buffer.
- `go-agent-loop/pkg/messages.TypedBuffer.ReadContext` is an additive
  context-first blocking read. The caller owns cancellation and timeout
  behavior through the supplied context, and cancellation returns an inspectable
  `ctx.Err()` error.
- `go-agent-loop/pkg/messages.TypedBuffer.ReadBlockingContext` remains as the
  legacy `(T, bool)` compatibility helper and does not distinguish
  cancellation from another closed `done` signal.
- `go-agent-loop/pkg/agentloop.ExecuteResult.FinalText()` returns an explicit
  `FinalTextResult` with `Status`, `Text`, `Err`, and `Partial`. It
  distinguishes non-empty success, explicit empty success, no final assistant
  text, caller-owned cancellation or deadline, terminal failure, and partial
  output with an error. `ExecuteResult.Text()` remains the legacy string-only
  helper.
- `go-agent-loop/pkg/agentloop.Stream` exposes legacy `HasNext() bool` plus the
  additive `Outcome()` contract. New callers can distinguish open, drained,
  caller-closed, cancelled/deadline, terminal failure, and partial-output before
  failure states without inferring from `HasNext() == false`.
- `agent-cli/internal/agent.Executor.LoadSystemPrompt` remains the compatible
  resolved-prompt helper for existing internal CLI callers.
- `agent-cli/internal/agent.Executor.LoadSystemPromptWithDetails` is the
  additive prompt-resolution inspection contract. It returns the resolved
  prompt plus `PromptResolutionDetails` entries for literal prompt text, prompt
  file reads, default `AGENTS.md` creation/read, config-backed system-info
  loading, runtime system-info collection, skill metadata reads, and loop suffix
  appends.
- `agent-cli/internal/agent.buildProviderHTTPRuntime(...)` owns stateless
  provider HTTP runtime composition. `WithProviderHTTPBaseTransport(...)`
  injects the live base transport before provider construction; record mode
  wraps that same injected transport, and replay mode replaces live transport
  with fixture replay.
- `docs/architecture/dependencies.md` documents the intended dependency
  direction and the Phase 2 constructor/runtime ownership boundaries already
  repaired for tool execution and stateless provider HTTP runtime wiring.

## Closure Map

| Row | Decision | Current evidence | Remaining repair work |
| --- | --- | --- | --- |
| `P4-CTX-*` | `uncertain` | The current public APIs show broad context-first cancellation coverage on `AgenticLoop`, `Inferencer`, `ToolExecutor`, `SessionInferencer`, `Session.Send`, and the new `TypedBuffer.ReadContext`. The exact reconciled `P4-CTX-*` row list is missing because `phase-4-api-audit-reconciliation` is not present in this checkout. | Import or link the reconciled audit row list, then close only the rows whose blocking public calls already return inspectable cancellation errors or have additive repairs. Future story work should verify cancellation outcomes for session/result helpers that still return only `bool` or zero values. |
| `P4-RESULT-*` | `uncertain` | `ExecuteResult.FinalText()` repairs the final text helper with explicit statuses for success, empty success, missing final text, cancellation/deadline, terminal failure, and partial output. `TypedBuffer.ReadContext` repairs one blocking buffer read with an error-returning cancellation contract. `Stream.Outcome()` repairs streaming lifecycle ambiguity for drained, closed, cancelled/deadline, terminal failure, and partial-output states. `docs/architecture/dependency-result-contracts.md` documents migration from the compatible legacy helpers. `Session.Send` and legacy `TypedBuffer.ReadBlockingContext` still expose `bool` outcomes with multiple documented meanings, and the exact reconciled `P4-RESULT-*` row list is missing from this checkout. | Import or link the reconciled audit row list. Later stories should add typed session send outcomes before marking any reconciled row that targets `Session.Send` as pass. |
| `P4-DI-*` | `uncertain` | Phase 2 evidence documents repaired ownership for `agentloop.New(...)` tool execution. This story narrows stateless provider HTTP runtime ownership with `WithProviderHTTPBaseTransport(...)`, proves the injected transport is used in live mode and wrapped in record mode, and keeps provider builders consuming `ProviderBuildContext.HTTPClient`. Prompt resolution now exposes `LoadSystemPromptWithDetails(...)` so the CLI-owned filesystem/config/system-info/skills/suffix inputs are observable without changing existing `LoadSystemPrompt(...)` callers. The exact reconciled `P4-DI-*` row list is unavailable in this checkout. | Import or link the reconciled audit row list. Remaining non-prompt dependency rows should be closed only against direct declarations, tests, or compatibility staging. |
| `P4-HYGIENE-*` | `uncertain` | `docs/architecture/dependencies.md` names intended public surfaces and incidental exports. No reconciled `P4-HYGIENE-*` artifact is available here to verify the requested row set. | Import or link the reconciled hygiene rows, then map each exported alias, package boundary, or doc-comment issue to current declarations and either close with evidence or stage follow-up. |
| `P4-API-01` | `uncertain` | `AgenticLoop`, `Inferencer`, `ToolExecutor`, `SessionInferencer`, and `TypedBuffer.ReadContext` use context-first signatures, so caller-owned cancellation is visible on major blocking surfaces repaired so far. `Stream.Outcome()` reports caller cancellation/deadline distinctly for streaming terminal state. Ambiguous helper outcomes remain on `Session.Send` and legacy `TypedBuffer.ReadBlockingContext`. | Define the exact `P4-API-01` row from the reconciled audit and close only the covered declarations. Later stories should add cancellation/result tests for remaining ambiguous helpers. |
| `P4-API-03` | `uncertain` | `ExecuteResult.FinalText()` removes the final text `string` ambiguity while keeping `ExecuteResult.Text()` compatible. `Stream.Outcome()` removes `HasNext() == false` ambiguity for clean drain, caller close, cancellation/deadline, terminal failure, and partial-output states while keeping `HasNext()` compatible. Credential-free tests prove empty success, no final message, terminal failure, cancellation, partial-output final results, and streaming lifecycle outcomes. Remaining lifecycle helpers still include ambiguous `bool` outcomes: `Session.Send` and legacy `TypedBuffer.ReadBlockingContext`. | Define the exact `P4-API-03` row from the reconciled audit, then close only covered declarations. Later stories should add additive typed outcomes for session send lifecycle states. |
| `P4-API-07` | `uncertain` | Dependency ownership is documented for intended module direction. Provider HTTP runtime ownership is explicit at the CLI composition seam: live mode uses the selected base transport, record mode wraps it, replay mode uses fixture replay, and provider constructors receive the resulting `*http.Client`. Prompt resolution ownership is explicit through `LoadSystemPromptWithDetails(...)`, which reports CLI-owned prompt file reads, default `AGENTS.md` create/read behavior, config/system-info reads, skills metadata reads, and suffix appends; the Agent CLI README documents the corresponding command behavior. | Define the exact `P4-API-07` row from the reconciled audit. If future work needs library-grade prompt composition, split pure prompt assembly from these IO-backed sources behind injected loaders without changing the documented CLI defaults. |
| `P4-GATE-01` | `uncertain` | Stories 001 through 007 now have implementation, public documentation, compatibility staging, and credential-free validation evidence for the repaired surfaces in this checkout. The public migration guide is `docs/architecture/dependency-result-contracts.md`. The exact reconciled row list from `phase-4-api-audit-reconciliation` is still not present here, so the gate cannot honestly be marked pass for every required `P4-CTX-*`, `P4-RESULT-*`, `P4-DI-*`, and `P4-HYGIENE-*` row. | Import or link the reconciled audit row list. Mark `P4-GATE-01` pass only after every required reconciled row is mapped to direct implementation evidence, public documentation, or explicit compatibility-staged follow-up. |

## Compatibility Staging

The current batch does not remove or change legacy public declarations. The
public migration note is
`docs/architecture/dependency-result-contracts.md`. The following
compatibility-sensitive repairs are staged for later work:

- Additive final-result contracts are implemented by
  `ExecuteResult.FinalText()` without removing `ExecuteResult.Text()`.
- Add typed session send outcome APIs without removing existing `Session.Send`
  `bool` behavior.
- `LoadSystemPromptWithDetails(...)` documents and exposes current prompt
  resolution filesystem, config, system-info, skills, and suffix side effects
  without changing `LoadSystemPrompt(...)` or CLI defaults. A future pure prompt
  assembly API can inject these sources if library consumers need it.
- Preserve existing stream iteration through `HasNext()`/`Response()` while using
  `Stream.Outcome()` for explicit terminal status in new integrations.

## Reviewer Command

Run the credential-free compile, prompt-resolution, and stream lifecycle checks
from the repository root:

```sh
go test ./agent-cli/internal/agent
go test ./go-agent-loop/pkg/agentloop
go test ./go-agent-loop/pkg/messages
make typecheck
make test
```
