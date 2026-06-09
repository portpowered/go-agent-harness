# Phase 4 Dependency Result Context Lifecycle Contract Map

This is the reviewer-facing convergence map for
`phase-4-dependency-result-context-lifecycle-contract`. It extends the earlier
`phase-4-dependency-result-contract-repair` evidence with the remaining
dependency, result, context, replay, prompt-resolution, and session-lifetime
surfaces that must be tracked before `P4-GATE-01` can close.

This map depends on `phase-4-audit-validator-015-reconciliation`. That
reconciled row artifact is not present in this checkout, so this document does
not mark `P4-GATE-01` as pass. Rows below use current declarations, docs, and
credential-free tests as evidence, and keep unresolved or compatibility-staged
work explicit.

## Surface Convergence

| Surface | Status | Public behavior and evidence | Checklist rows | Remaining work |
| --- | --- | --- | --- | --- |
| `agentloop.ExecuteResult.FinalText()` | Repaired | `go-agent-loop/pkg/agentloop/execute_result.go` exposes `FinalTextResult` with `Status`, `Text`, `Err`, and `Partial`. It distinguishes non-empty success, explicit empty success, missing final assistant text, caller cancellation/deadline, terminal failure, and partial output. `ExecuteResult.Text()` remains the compatible string-only helper. Tests in `go-agent-loop/pkg/agentloop/execute_result_test.go` and `agent_loop_test.go` cover these outcomes without provider credentials. | `P4-API-01`, `P4-API-03`, `P4-GATE-01` | None for the mapped final-text contract. Keep `Text()` compatibility until a future breaking release. |
| `agentloop.Stream.Outcome()` | Repaired | `go-agent-loop/pkg/agentloop/execute_result.go` exposes `StreamOutcome` with `Status`, `Err`, and `Partial`. It distinguishes open, drained, caller-closed, cancelled/deadline, and failed streams after iteration. Existing `HasNext()`/`Response()` callers continue to compile. Tests in `go-agent-loop/pkg/agentloop/execute_result_test.go` and `agent_loop_test.go` prove drained, close, cancellation, failure, and partial-output outcomes. | `P4-API-01`, `P4-API-03`, `P4-GATE-01` | Provider-authored end and loop-synthesized end are not yet a distinct public status; keep that gap compatibility-staged until story 003 repairs or documents it with final event evidence. |
| `messages.TypedBuffer.ReadContext(ctx)` | Repaired | `go-agent-loop/pkg/messages/buffers.go` adds an error-returning blocking read. Caller-owned cancellation and deadlines are visible through `ctx.Err()`, while legacy `ReadBlockingContext(ctx) (T, bool)` is retained. Tests in `go-agent-loop/pkg/messages/buffers_test.go` distinguish successful reads from cancellation before and during a blocked read. | `P4-API-01`, `P4-API-03`, `P4-GATE-01` | Add typed write/send outcomes for buffer-full and closed-session cases in story 002; `ReadBlockingContext` remains compatibility-only. |
| `messages.Session.Send(ctx, msg) bool` and provider/replay send methods | Compatibility-staged | `go-agent-loop/pkg/messages/session.go` documents the current bool outcome: `false` may mean caller cancellation or full outbound buffer. `go-llm-gateway/pkg/testing.SessionReplayer.Send`, `providers/openai.realtimeSession.Send`, and `providers/grok.grokSession.Send` keep that interface for compatibility. Replay divergence is inspectable through `SessionReplayer.Err()`, but send callers still cannot branch on cancellation versus closed session, buffer-full, or terminal failure from the send return alone. | `P4-API-01`, `P4-API-03`, `P4-GATE-01` | Story 002 should add an additive typed send outcome or error-returning variant for success, caller cancellation, buffer-full, closed session, timeout, and terminal failure where observable. |
| `go-llm-gateway/pkg/testing.SessionReplayer` replay divergence and incomplete replay | Already correct for typed mismatch errors; open for richer public outcome shape | `SessionReplayer.Send` terminates divergent replay and `SessionReplayer.Err()` returns an error wrapping `providers.ErrReplayMismatch` and `gateway.NewReplayMismatchError(...)`. `SessionReplayer.Close()` records omitted expected outbound events as replay mismatch. Tests in `go-llm-gateway/pkg/testing/session_replay_test.go` and `session_websocket_dialer_test.go` assert divergence and incomplete replay with `errors.Is` rather than log parsing. | `P4-API-02`, `P4-API-03`, `P4-API-05`, `P4-GATE-01` | Story 004 should decide whether the public replay contract needs a first-class outcome value in addition to typed errors and `Err()`. |
| `agent-cli/internal/agent.Executor.LoadSystemPromptWithDetails(...)` | Repaired for CLI observability | The public CLI-facing migration guide in `docs/architecture/dependency-result-contracts.md` names prompt resolution as CLI-owned composition. `LoadSystemPromptWithDetails(...)` reports literal prompts, prompt file reads, default `AGENTS.md` create/read behavior, config system-info loading, runtime system-info collection, skill metadata reads, and loop suffix appends. Existing `LoadSystemPrompt(...)` remains compatible. | `P4-API-07`, `P4-GATE-01` | Story 006 should decide whether to introduce a pure library-grade prompt composition API or keep this CLI-owned IO boundary as the documented compatibility stage. |
| `go-llm-gateway/pkg/models.SessionConfig` and `gateway.DefaultSessionGateway.ConnectSession(ctx, config)` | Already correct at gateway/provider boundary | `models.SessionConfig` is the persistent session shape passed into `gateway.DefaultSessionGateway.ConnectSession(ctx, config)` and concrete provider `ConnectSession(ctx, config)` methods. The operation context is separate from the config value at this boundary. Tests in `go-llm-gateway/pkg/inference/session_inferencer_test.go`, `providers/openai/session_test.go`, and `providers/grok/provider_test.go` cover gateway/provider session establishment through local fakes and replay fixtures. | `P4-API-01`, `P4-API-07`, `P4-GATE-01` | Story 005 should repair the loop-facing `SessionGatewayInferencer` options-only adapter if richer persistent session shape needs to cross from loop integrations without provider-specific construction. |
| `go-llm-gateway/pkg/inference.SessionGatewayInferencer` | Open / compatibility-staged | `NewSessionGatewayInferencer(...)` stores persistent model, voice, and instructions as options and later calls `ConnectSession(ctx)` with a constructed `models.SessionConfig`. This separates the caller-owned operation context from stored adapter shape, but the adapter cannot express the full `models.SessionConfig` fields already available at the gateway boundary. | `P4-API-01`, `P4-API-07`, `P4-GATE-01` | Story 005 should define one loop-facing request/config contract that preserves caller-owned `context.Context` while allowing the persistent session shape to remain explicit and reusable. |
| Provider-authored end vs loop-synthesized end | Compatibility-staged gap | Current stream and final-text status contracts distinguish clean drain, cancellation, timeout, terminal failure, empty success, missing final text, and partial output. They do not yet expose whether a terminal assistant message came directly from the provider or was synthesized by loop completion logic. | `P4-API-03`, `P4-GATE-01` | Story 003 should add a public status, metadata field, emitted event, or explicit compatibility note naming affected declarations and migration guidance. |

## Checklist Decisions

| Row | Decision | Evidence | Follow-up before closure |
| --- | --- | --- | --- |
| `P4-API-01` | `uncertain` | Major blocking surfaces already accept caller-owned `context.Context`: loop execution/streaming, gateway inference/session connection, provider session connection, `Session.Send`, and `TypedBuffer.ReadContext`. `FinalText()` and `Stream.Outcome()` expose cancellation/deadline as terminal outcomes. | Typed send outcomes must distinguish cancellation from full buffers and closed sessions. The loop-facing session config story must preserve the context/config lifetime split. |
| `P4-API-03` | `uncertain` | `FinalText()` and `Stream.Outcome()` repair final text and stream terminal ambiguity. Replay mismatch and incomplete replay are inspectable via typed errors and `Err()`. | Session send remains bool-only, provider-authored versus loop-synthesized terminal messages remain indistinguishable, and story 004 must decide whether replay needs a public outcome value beyond typed errors. |
| `P4-API-07` | `uncertain` | Provider HTTP runtime and prompt resolution ownership are documented in `docs/architecture/dependency-result-contracts.md`; `models.SessionConfig` is explicit at the gateway/provider boundary; prompt details expose CLI IO side effects. | Prompt composition remains CLI-owned unless story 006 introduces an injected pure API. `SessionGatewayInferencer` still exposes only model, voice, and instructions instead of the full persistent session shape. |
| `P4-GATE-01` | `uncertain` | This map, the public dependency/result guide, and existing credential-free tests give reviewer-visible evidence for repaired and staged contracts. | Do not close until `phase-4-audit-validator-015-reconciliation` rows are available and stories 002 through 007 either repair or explicitly compatibility-stage their remaining surfaces with validation evidence. |

## Reviewer Commands

Run these credential-free checks from the repository root:

```sh
make typecheck
go test ./go-agent-loop/pkg/agentloop
go test ./go-agent-loop/pkg/messages
go test ./go-llm-gateway/pkg/testing
go test ./go-llm-gateway/pkg/inference
go test ./agent-cli/internal/agent
```
