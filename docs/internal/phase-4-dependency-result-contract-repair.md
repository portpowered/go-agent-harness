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
- `go-agent-loop/pkg/agentloop.ExecuteResult.Text()` returns `string`; the
  current public declaration does not distinguish no assistant text, successful
  empty assistant text, cancellation, or terminal failure.
- `go-agent-loop/pkg/agentloop.Stream` exposes `HasNext() bool` plus `Err()`;
  clean exhaustion and `Close()` both produce `HasNext() == false`, while
  terminal errors are inspectable only through `Err()`.
- `agent-cli/internal/agent.Executor.LoadSystemPrompt` is exported inside an
  `internal` package and performs filesystem and config reads through
  `loadSystemPrompt`; default prompt resolution may create or read `AGENTS.md`
  through `workspace.EnsureAgentsMD`.
- `docs/architecture/dependencies.md` documents the intended dependency
  direction and the Phase 2 constructor/runtime ownership boundaries already
  repaired for tool execution and stateless provider HTTP runtime wiring.

## Closure Map

| Row | Decision | Current evidence | Remaining repair work |
| --- | --- | --- | --- |
| `P4-CTX-*` | `uncertain` | The current public APIs show broad context-first cancellation coverage on `AgenticLoop`, `Inferencer`, `ToolExecutor`, `SessionInferencer`, `Session.Send`, and the new `TypedBuffer.ReadContext`. The exact reconciled `P4-CTX-*` row list is missing because `phase-4-api-audit-reconciliation` is not present in this checkout. | Import or link the reconciled audit row list, then close only the rows whose blocking public calls already return inspectable cancellation errors or have additive repairs. Future story work should verify cancellation outcomes for session/result helpers that still return only `bool` or zero values. |
| `P4-RESULT-*` | `fail` | `ExecuteResult.Text()` returns only `string`; empty text can mean no final answer or successful empty content. `Session.Send` and `TypedBuffer.ReadBlockingContext` still expose `bool` outcomes with multiple documented meanings. | Add explicit result or error-returning contracts for final text/result helpers and at least one buffer/session operation. Preserve legacy helpers and document migration. |
| `P4-DI-*` | `uncertain` | Phase 2 evidence documents repaired ownership for `agentloop.New(...)` tool execution and stateless provider HTTP runtime injection in `agent-cli`. Prompt resolution still mixes filesystem/config/skills reads in `Executor.LoadSystemPrompt`. The exact reconciled `P4-DI-*` row list is unavailable in this checkout. | Import or link the reconciled audit row list. For remaining rows, either make side-effect dependencies caller-owned or document compatibility-staged ownership with affected declarations. Prompt resolution needs explicit filesystem/environment ownership evidence in a later story. |
| `P4-HYGIENE-*` | `uncertain` | `docs/architecture/dependencies.md` names intended public surfaces and incidental exports. No reconciled `P4-HYGIENE-*` artifact is available here to verify the requested row set. | Import or link the reconciled hygiene rows, then map each exported alias, package boundary, or doc-comment issue to current declarations and either close with evidence or stage follow-up. |
| `P4-API-01` | `uncertain` | `AgenticLoop`, `Inferencer`, `ToolExecutor`, `SessionInferencer`, and `TypedBuffer.ReadContext` use context-first signatures, so caller-owned cancellation is visible on major blocking surfaces repaired so far. Ambiguous helper outcomes remain on `Session.Send`, `Stream.HasNext`, legacy `TypedBuffer.ReadBlockingContext`, and final text helpers. | Define the exact `P4-API-01` row from the reconciled audit and close only the covered declarations. Later stories should add cancellation/result tests for remaining ambiguous helpers. |
| `P4-API-03` | `fail` | Current final-result and lifecycle helpers still include ambiguous `string` and `bool` outcomes: `ExecuteResult.Text()`, `Session.Send`, `TypedBuffer.ReadBlockingContext`, and `Stream.HasNext`. | Add additive typed outcomes for final results and buffer/session lifecycle states, with credential-free tests for empty success, terminal failure, cancellation, and closed/drained states as applicable. |
| `P4-API-07` | `uncertain` | Dependency ownership is documented for intended module direction and the previously repaired constructor/runtime seams. The CLI prompt resolution seam still performs user-facing filesystem/config side effects from `LoadSystemPrompt`. | Define the exact `P4-API-07` row from the reconciled audit, then either repair or compatibility-stage prompt resolution, provider runtime, filesystem, environment, process, transport, time, and session dependency ownership decisions. |
| `P4-GATE-01` | `fail` | This story publishes the current map, but result ambiguity and missing reconciled-audit evidence remain. The final gate cannot pass until all required `P4-CTX-*`, `P4-RESULT-*`, `P4-DI-*`, and `P4-HYGIENE-*` rows have direct implementation or compatibility-staging evidence. | Complete stories 002 through 007, update this evidence with concrete row decisions, and run the required credential-free quality gate. |

## Compatibility Staging

The current batch does not remove or change legacy public declarations. The
following compatibility-sensitive repairs are staged for later stories:

- Add explicit final-result contracts without removing `ExecuteResult.Text()`.
- Add typed session outcome APIs without removing existing `bool` methods.
- Document or inject prompt-resolution filesystem, environment, config, system
  info, and skills dependencies without changing current CLI defaults.
- Preserve existing stream iteration while adding explicit terminal status if a
  later story repairs `Stream.HasNext` ambiguity.

## Reviewer Command

Run the credential-free compile check from the repository root:

```sh
make typecheck
```
