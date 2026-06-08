# Contract Gap Audit

## Phase 4 API Audit Reconciliation Map

This section is the row-level Phase 4 source of truth for P4-API-01 through P4-API-07 and P4-GATE-01. The older findings below remain evidence and planning context, but audit documentation alone cannot close implementation checklist rows; a row is closable only when the named public contract, runtime behavior, docs/examples, and deterministic validation evidence exist in code or tests.

Status values:

- `pass`: current implementation and deterministic evidence are sufficient for this audit row.
- `fail`: current implementation evidence shows an observable contract gap.
- `uncertain`: evidence exists, but the row still needs targeted reconciliation before a reviewer should treat it as closed.
- `open`: intentionally not closable in this audit-only lane.

### P4-API-01: Dependency Ownership And Injection Contracts

- Outcome: `fail`
- Affected public packages: `go-agent-loop/pkg/agentloop`, `go-agent-loop/pkg/messages`, `go-llm-gateway/pkg/gateway`, `go-llm-gateway/pkg/inference`, `agent-cli/internal/agent`, `agent-cli/internal/services`
- Exported declarations: `agentloop.New`, `agentloop.WithInferencer`, `agentloop.WithSessionInferencer`, `agentloop.WithToolExecutor`, `agentloop.WithToolExecutionDisabled`, `messages.Inferencer`, `messages.SessionInferencer`, `messages.Session`, `gateway.NewGateway`, `gateway.NewSessionGateway`, `inference.NewGatewayInferencer`, `inference.NewSessionGatewayInferencer`, `agent.ProviderFactory`, `agent.Executor`, `services.RunSession`
- Observable contract issue: tool execution, HTTP transport ownership, and scoped session runtime planning now have explicit caller/composition seams, but the row remains failed because prompt resolution and CLI session/config acquisition still have caller-observable filesystem, environment, live dialer, and config-loading side effects that are not expressed as injectable dependencies at the public or composition contract boundary.
- Implementation evidence:
  - `agentloop.New` fails construction when tools are advertised without either `WithToolExecutor` or `WithToolExecutionDisabled`, so tool capability is no longer silently supplied by the loop constructor.
  - `messages.Inferencer` and `messages.SessionInferencer` keep model/session execution injected into the loop, and `inference.NewGatewayInferencer` plus `inference.NewSessionGatewayInferencer` adapt gateway-owned implementations into those loop interfaces.
  - `agent-cli/internal/agent.buildProviderHTTPRuntime` centralizes live, record, and replay HTTP client construction before provider builders run, so provider builders consume an owned `ProviderBuildContext.HTTPClient`.
  - `agent-cli/internal/services/session_runtime.go` centralizes Grok/OpenAI session runtime planning, replay/record dialer selection, and capture finalization behind one CLI composition seam.
- Planning-only evidence: DI-01 and DI-02 are reconciled as repaired for their scoped constructor and transport ownership claims; DI-03 remains open for prompt-resolution filesystem/config/skills side effects; DI-04 and HC-03 are narrowed to the remaining session/config/dialer ownership decisions that are still application wiring rather than provider-neutral public contracts.
- Docs/tests/examples evidence: `go-agent-loop/pkg/agentloop/agent_loop_test.go` proves explicit tool-execution decisions; `agent-cli/internal/agent/provider_runtime_test.go` proves live, record, and replay HTTP runtime ownership; `agent-cli/internal/services/session_runtime_test.go` proves injected, replay, Grok record, and OpenAI record planning paths; `go-llm-gateway/pkg/inference/session_inferencer_test.go` and `main_inferencer_test.go` prove gateway-to-loop adapter ownership.
- Remaining repair slice: split `agent.Executor.loadSystemPrompt` into pure prompt assembly plus injected filesystem/config/system-info/skills loaders; document whether `services.RunSession` is an internal composition contract or only CLI behavior; make missing live dialers, config files, replay captures, and provider construction failures observable through stable dependency/result errors rather than only wrapped text; keep network and filesystem side effects owned at explicit CLI composition entrypoints.
- Reviewer commands: from the repository root, run `make typecheck` to prove the public packages still compile; run `(cd go-agent-loop && go test ./pkg/agentloop)` to verify constructor ownership; run `(cd agent-cli && go test ./internal/agent ./internal/services)` to verify CLI-owned provider and session runtime seams.

### P4-API-02: Typed Errors And Stream Failure Contracts

- Outcome: `fail`
- Affected public packages: `go-agent-loop/pkg/messages`, `go-agent-loop/pkg/participants`, `go-agent-loop/pkg/subsystems`, `go-llm-gateway/pkg/gateway`, `go-llm-gateway/pkg/testing`, `go-llm-gateway/pkg/providers/openai`, `go-llm-gateway/pkg/providers/anthropic`, `go-llm-gateway/pkg/providers/gemini`, `go-llm-gateway/pkg/providers/grok`, `agent-cli/internal/services`
- Exported declarations: `messages.ErrorValue`, `messages.NewErrorValue`, `messages.NewErrorValueWithDetails`, `messages.StreamTypeError`, `messages.Session`, `messages.SessionInferencer`, `gateway.InteractionError`, `gateway.InteractionEventError`, `gateway.InteractionCancellation`, `testing.NewSessionReplayer`, `testing.WithReplayContext`
- Observable contract issue: the shared stream contract has typed error fields and selected gateways preserve structured error data, but the row remains failed because loop participants, stream adapters, interaction-event bridging, and CLI session command paths still have caller-visible paths that collapse failures into `err.Error()` text or phase-prefixed Go errors without a stable taxonomy.
- Implementation evidence:
  - `messages.ErrorValue` exposes `Message`, `ErrorType`, `Code`, `Param`, and `EventID`; `messages.NewErrorValueWithDetails` preserves provider-supplied OpenAI Realtime error metadata.
  - `gateway.Interact` emits normalized PNIG `InteractionError` events with codes such as `tool_result_validation_error`, `provider_timeout`, and `provider_error`, and emits `InteractionCancellation` with `caller_cancelled` for canceled contexts.
  - `go-llm-gateway/pkg/testing.SessionReplayer` detects replay divergence and omitted outbound events deterministically; `testing.WithReplayContext` stops timed replay delivery when the owned lifecycle context is canceled.
  - `agent-cli/internal/services` preserves `context.Canceled` through record and replay command paths, and renders in-band session `ERROR` messages as returned command errors.
- Planning-only evidence: ERR-01, ERR-02, LIFECYCLE-01, LIFECYCLE-02, and COMPAT-03 below remain open planning findings for unifying typed stream errors, session command failure categories, and completion/error boundaries.
- Docs/tests/examples evidence:
  - `go-llm-gateway/pkg/providers/openai/session_test.go` asserts OpenAI Realtime error detail preservation through `ErrorValue`.
  - `go-llm-gateway/pkg/gateway/interaction_gateway_test.go` asserts PNIG provider errors, timeout errors, and caller cancellation events.
  - `go-llm-gateway/pkg/testing/session_replay_test.go` asserts replay divergence, omitted outbound events, and cancellation-stopped replay delivery.
  - `agent-cli/internal/services/session_test.go` asserts record and replay cancellation preserve `context.Canceled` and stop timed replay output.
  - `go-agent-loop/pkg/participants/model_runner_test.go`, `go-agent-loop/pkg/participants/tool_runner_test.go`, and `go-llm-gateway/pkg/providers/anthropic/stream_test.go` prove selected `ERROR` stream handling, while also showing remaining string-only `NewErrorValue(err.Error())` paths.
- Remaining repair slice: keep P4-ERR-01 open to define shared caller-actionable stream error classes and convert loop/provider/interaction bridge emitters away from string-only `NewErrorValue(err.Error())`; keep P4-ERR-02 open to define typed CLI/session categories for validation, provider-connect, replay-divergence, provider-runtime, cancellation, and capture-persist failures; keep P4-ERR-03 and stream lifecycle work open to document which failures are emitted in-band as `ERROR` stream messages, which return Go errors, and how `MESSAGE.END`, cancellation, replay divergence, and provider stream errors compose.
- Reviewer commands: from the repository root, run `(cd go-llm-gateway && go test ./pkg/gateway ./pkg/testing ./pkg/providers/openai ./pkg/providers/anthropic ./pkg/providers/gemini ./pkg/providers/grok)` to verify deterministic typed-error, replay, cancellation, and provider stream evidence; run `(cd go-agent-loop && go test ./pkg/participants ./pkg/subsystems ./test/functional)` to verify loop stream error and session behavior; run `(cd agent-cli && go test ./internal/services)` to verify CLI session replay/record cancellation and command error behavior.

### P4-API-03: Result, Lifecycle, And Completion Contracts

- Outcome: `fail`
- Affected public packages: `go-agent-loop/pkg/agentloop`, `go-agent-loop/pkg/messages`, `go-agent-loop/pkg/participants`, `go-llm-gateway/pkg/inference`, `go-llm-gateway/pkg/gateway`, `agent-cli/internal/output`, `agent-cli/internal/agent`
- Exported declarations: `agentloop.ExecuteResult`, `agentloop.Stream`, `agentloop.StreamingExecuteResult`, `messages.Message`, `messages.StreamMessage`, `messages.StreamMessageType`, `messages.Session`, `gateway.InferenceResponse`, `gateway.InteractionEvent`, `gateway.InteractionEventEnd`, `gateway.InteractionEventError`, `gateway.InteractionEventCancellation`
- Observable contract issue: callers can observe normalized messages and stream events, but result completion semantics still differ between non-streaming `ExecuteResult.Text`, loop-synthesized stream fallback boundaries, provider-authored `MESSAGE.END`, PNIG `interaction.end`, CLI stop conditions, replay completion, and session `Done`/`Close` boundaries. The public contract does not yet state which terminal signal is authoritative for each mode.
- Implementation evidence:
  - `ExecuteResult.Messages` records produced turn messages, and `ExecuteResult.Text` returns the last assistant text message without pending tool calls or reasoning-only content.
  - `StreamingExecuteResult.EventStream` exposes typed deltas and `Messages()` blocks until loop completion; `Stream.Close` drains the producer channel to avoid blocking producers.
  - `participants.ModelRunner` normalizes provider stream output and can synthesize `MESSAGE.END` when an upstream stream closes without one or when non-streaming fallback output is converted into deltas.
  - `gateway.Interact` emits normalized PNIG start, text delta, final message, cancellation, error, usage, and end events with sequence numbers.
- Planning-only evidence: LIFECYCLE-01 and LIFECYCLE-02 remain the current source of the lifecycle ambiguity; COMPAT-01 and COMPAT-02 name the fixture and CLI compatibility risk; CTX-01 and CTX-02 explain why per-session result context is still split across constructor options, caller context, and replay/record wrappers.
- Docs/tests/examples evidence: `go-agent-loop/pkg/agentloop/execute_result.go` documents result and stream accessors; `go-agent-loop/pkg/participants/model_runner_test.go` covers synthesized and in-band stream completion behavior; `go-agent-loop/test/functional/session_lifecycle_test.go` and `session_termination_test.go` cover selected session lifecycle paths; `go-llm-gateway/pkg/gateway/interaction_gateway_test.go` covers PNIG terminal event behavior; `agent-cli/internal/output/json_stream_test.go` and `binary_stream_test.go` cover CLI-visible output framing.
- Remaining repair slice: define caller-visible terminal states for `Execute`, `ExecuteStreaming`, provider-authored streams, synthesized fallback streams, `messages.Session.Done`, `messages.Session.Close`, replay completion, capture flush, and PNIG cancellation/error/end sequences; stage compatibility by updating recorded fixtures and CLI replay/record tests whenever stop conditions or terminal event provenance changes.
- Reviewer commands: from the repository root, run `(cd go-agent-loop && go test ./pkg/engine ./test/functional)` to verify loop result reconstruction and lifecycle behavior; run `(cd go-llm-gateway && go test ./pkg/gateway)` to verify PNIG terminal event behavior.

### P4-API-04: Provider Capability Discovery And Unsupported-Feature Validation

- Outcome: `fail`
- Affected public packages: `agent-cli/internal/config`, `agent-cli/internal/input`, `agent-cli/internal/agent`, `go-llm-gateway/pkg/providers`, `go-llm-gateway/pkg/providers/openai`, `go-llm-gateway/pkg/providers/anthropic`, `go-llm-gateway/pkg/providers/gemini`, `go-llm-gateway/pkg/providers/fal`, `go-llm-gateway/pkg/providers/grok`
- Exported declarations: `config.ModelInfo`, `(*config.ModelInfo).SupportsOutputModality`, `(*config.ModelInfo).SupportsInputMimeType`, `config.ModelsConfig.Lookup`, `input.ValidateMimeType`, `input.ValidateContentPartsMimeTypes`, `agent.Executor.RunAsk`, `providers.Provider`, `providers.SessionProvider`, `providers.InferenceRequest`, `providers.ThinkingConfig`, `providers.CacheControlConfig`
- Observable contract issue: current implementation has starter evidence for local model metadata and selected provider-specific request options, but the public gateway/provider surface still lacks one provider-neutral capability discovery and unsupported-feature validation contract that callers can query before invoking tools, streaming, sessions, audio, image input, video output, reasoning, prompt caching, or provider-specific config. Unknown models and unavailable config intentionally fall through to provider runtime behavior in CLI validation, so absence of local metadata is not a caller-visible unsupported-feature guarantee.
- Implementation evidence:
  - Supported locally: `config.ModelInfo` records `InputModalities`, `OutputModalities`, `SupportsToolUse`, `SupportsReasoning`, tokenizer, provider IDs, aliases, and `SupportedInputMimeTypes`; `SupportsOutputModality`, `SupportsInputMimeType`, and `ModelsConfig.Lookup` provide deterministic metadata checks.
  - Unsupported locally: `agent.Executor.RunAsk` calls `validateOutputModality` and `validateInputMimeTypes`, which reject non-text output modalities and input MIME types when the selected OpenAI-compatible model is known in `models.yaml`.
  - Unknown locally: CLI validation skips enforcement when model config cannot be loaded, the active provider is not OpenAI-compatible, `runData.Models` is absent, or the model is missing from `ModelsConfig`; those paths deliberately let the provider decide.
  - Provider-specific evidence: `providers.InferenceRequest.Tools` is accepted by stateless providers that implement tool translation; `Provider.InferStream` is the only streaming affordance on the provider interface; `SessionProvider.ConnectSession` is the only session capability seam; `ThinkingConfig` is mapped by Anthropic and ignored by OpenAI; `CacheControlConfig` is mapped by Anthropic while OpenAI keeps default cache behavior; `InferenceRequest.Config` is passed through by fal.ai for model-specific options.
  - Capability status by requested feature: tools are `supported` in CLI metadata and provider request shapes but `uncertain` as provider-neutral discovery; streaming is `supported` as a method shape and selected provider tests but `uncertain` for per-model availability; sessions are `supported` only where a `SessionProvider` is wired and `unknown` elsewhere; audio input is `supported` by Gemini/fal metadata and fal tests but `uncertain` in provider-neutral discovery; image input is `supported` by metadata and provider conversion tests; video output is `supported` by fal response tests but `uncertain` as CLI output metadata beyond model rows; reasoning is `supported` by metadata and Anthropic request mapping but `unsupported` by OpenAI mapping; prompt caching is `supported` by Anthropic mapping, `unknown` for provider-neutral discovery, and OpenAI remains provider-default only; provider-specific config is `supported` through fal `InferenceRequest.Config` passthrough and `unknown` for other provider families.
- Planning-only evidence: P4-CAP-01 is reconciled from "capability discovery absent" to "provider-neutral discovery still absent even though CLI metadata and selected provider option mappings exist"; P4-VALIDATION-01 is reconciled from "unsupported-feature validation absent" to "CLI-local OpenAI-compatible output and MIME validation exists, but gateway/provider validation remains partial and mostly runtime/provider-owned."
- Docs/tests/examples evidence:
  - `agent-cli/internal/config/models_test.go` verifies output modality checks, input MIME metadata, default multimodal MIME coverage, lookup behavior, and unknown-model allow behavior.
  - `agent-cli/internal/input/validate_test.go` verifies MIME rejection errors, supported-list behavior, conversion hints, and validation across image, audio, video, and file parts.
  - `agent-cli/internal/agent/executor.go` contains the observable validation flow through `RunAsk`, `validateOutputModality`, and `validateInputMimeTypes`.
  - `go-llm-gateway/pkg/providers/openai/params_test.go` verifies OpenAI option mapping and that `Thinking` is ignored.
  - `go-llm-gateway/pkg/providers/anthropic/params_test.go` verifies `ThinkingConfig` and `CacheControlConfig` mapping.
  - `go-llm-gateway/pkg/providers/gemini/replay_test.go`, `go-llm-gateway/pkg/providers/gemini/stream_test.go`, and `go-llm-gateway/pkg/providers/gemini/models_test.go` verify deterministic Gemini tool, image/audio content, and streaming behavior.
  - `go-llm-gateway/pkg/providers/fal/provider_test.go` verifies audio-to-video, image-to-video, TTS, unsupported model errors, and `Config` passthrough without live provider credentials.
  - `go-llm-gateway/pkg/providers/grok/provider_test.go` and `go-llm-gateway/pkg/providers/openai/session_test.go` verify credential-free session construction behavior through injected dialers.
- Remaining repair slice: keep P4-CAP-01 open to define a public capability discovery surface that returns supported, unsupported, and unknown for each provider/model pair across tools, streaming, sessions, audio, image input, video output, reasoning, prompt caching, and provider-specific config; keep P4-VALIDATION-01 open to move unsupported-feature validation to the reusable gateway/provider boundary, add credential-free fixtures for OpenAI-compatible, Anthropic, Gemini, fal.ai, and Grok capability decisions, and document when unknown capabilities are intentionally deferred to the provider.
- Reviewer commands: from the repository root, run `(cd agent-cli && go test ./internal/config ./internal/input ./internal/agent)` to verify CLI-local model metadata, MIME validation, and executor validation behavior; run `(cd go-llm-gateway && go test ./pkg/providers/openai ./pkg/providers/anthropic ./pkg/providers/gemini ./pkg/providers/fal ./pkg/providers/grok)` to verify provider-local deterministic option, modality, stream, session, and provider-specific config behavior; run `go doc ./go-llm-gateway/pkg/providers` to inspect the current public provider declarations and confirm that no provider-neutral capability discovery method exists.

### P4-API-05: Public Gateway, Provider, And Session Surface Alignment

- Outcome: `uncertain`
- Affected public packages: `go-llm-gateway/pkg/gateway`, `go-llm-gateway/pkg/inference`, `go-llm-gateway/pkg/models`, `go-llm-gateway/pkg/providers`, `go-agent-loop/pkg/messages`
- Exported declarations: `gateway.InferenceRequest`, `gateway.InferenceResponse`, `gateway.DefaultGateway`, `gateway.DefaultSessionGateway`, `gateway.InteractionRequest`, `gateway.InteractionEvent`, `inference.GatewayInferencer`, `inference.SessionGatewayInferencer`, `models.Message`, `models.SessionConfig`, `providers.InferenceRequest`, `providers.InferenceResponse`
- Observable contract issue: gateway-facing packages expose stateless, session, and interaction surfaces, but `pkg/models` still aliases loop-owned message contracts and the audit has not yet identified which package owns compatibility for each public declaration.
- Implementation evidence: `go-llm-gateway/pkg/models/message.go` re-exports loop message types; `go-llm-gateway/pkg/gateway` exposes provider-neutral stateless, session, and PNIG interaction contracts; `go-llm-gateway/pkg/inference` adapts gateway contracts to loop interfaces.
- Planning-only evidence: HC-02 and DOC-01 below describe the naming facade risk and missing compatibility-boundary explanation.
- Docs/tests/examples evidence: `go-llm-gateway/pkg/inference/main_inferencer_test.go`, `go-llm-gateway/pkg/inference/session_inferencer_test.go`, and `go-llm-gateway/pkg/gateway/interaction_types_test.go` exercise the adapters and JSON/event shapes.
- Remaining repair slice: declare whether `pkg/models` remains a compatibility alias layer or becomes gateway-owned vocabulary; align docs, package comments, and tests with that ownership decision.
- Reviewer commands: from the repository root, run `(cd go-llm-gateway && go test ./pkg/inference ./pkg/gateway)` to verify gateway/inference contract alignment; run `go doc ./go-llm-gateway/pkg/models` to inspect exported alias documentation.

### P4-API-06: Context, Cancellation, Timeout, And Retry Semantics

- Outcome: `fail`
- Affected public packages: `go-agent-loop/pkg/messages`, `go-agent-loop/pkg/agentloop`, `go-agent-loop/pkg/participants`, `go-llm-gateway/pkg/testing`, `go-llm-gateway/pkg/inference`, `go-llm-gateway/pkg/gateway`, `agent-cli/internal/services`
- Exported declarations: `messages.Inferencer.Infer`, `messages.Inferencer.InferStream`, `messages.SessionInferencer.ConnectSession`, `messages.Session.Send`, `messages.Session.Close`, `messages.Session.Done`, `agentloop.AgenticLoop.Execute`, `agentloop.AgenticLoop.ExecuteStreaming`, `gateway.Gateway.Infer`, `gateway.Gateway.InferStream`, `gateway.Gateway.Interact`, `testing.WithReplayContext`, `testing.NewRecordSessionInferencer`, `testing.NewReplaySessionInferencer`, `services.RunSession`
- Observable contract issue: cancellation is accepted by the main public calls and selected replay/interaction paths preserve it, but timeout and retry ownership remains package-local: CLI session mode owns fixed command timeouts, PNIG maps caller cancellation into an event, provider stream adapters generally return Go errors or close channels, and no shared contract states whether retry belongs to callers, gateway adapters, provider implementations, or CLI orchestration.
- Implementation evidence:
  - `messages.Inferencer`, `messages.SessionInferencer`, `gateway.Gateway`, and `agentloop.AgenticLoop` methods all accept `context.Context` for caller-owned cancellation/deadlines.
  - `testing.WithReplayContext` binds replay delivery to an explicit lifecycle context, and session replay/record inferencer wrappers bind relay lifetime to `ConnectSession(ctx)`.
  - `gateway.Interact` emits `InteractionCancellation{Reason: "caller_cancelled"}` when the caller context is canceled and emits timeout/provider error events for selected PNIG failures.
  - `agent-cli/internal/services/session_runtime.go` and `session.go` preserve `context.Canceled` in record/replay command paths and use explicit per-mode `MaxDuration` values for command-level timeout behavior.
- Planning-only evidence: CTX-01 remains open because session shape is still split between constructor options and caller context; CTX-02 is narrowed to the repaired replay/record relay lifecycle context; LIFECYCLE-01, LIFECYCLE-02, and COMPAT-02 remain open for stop-condition and fixture compatibility staging.
- Docs/tests/examples evidence: `go-llm-gateway/pkg/testing/session_replay_test.go` covers replay cancellation, divergence, and omitted outbound behavior; `go-llm-gateway/pkg/testing/session_record_test.go` covers recorder lifecycle behavior; `go-llm-gateway/pkg/gateway/interaction_gateway_test.go` covers PNIG cancellation and timeout events; `agent-cli/internal/services/session_test.go` covers CLI record/replay cancellation preservation; `go-agent-loop/test/functional/run_test.go` covers loop cancellation exit.
- Remaining repair slice: document per-surface ownership for caller cancellation, command timeout, provider timeout, retry policy, and terminal event behavior across stateless inference, streaming inference, PNIG interactions, realtime sessions, replay, and record mode; add deterministic tests for any retry/timeout guarantees introduced, and keep retry behavior opt-in rather than hidden behind providers or CLI services.
- Reviewer commands: from the repository root, run `(cd go-llm-gateway && go test ./pkg/testing ./pkg/gateway)` to verify replay/record and interaction cancellation behavior; run `(cd go-agent-loop && go test ./test/functional -run 'TestRun_ExitsOnContextCancellation|TestSession')` to verify loop cancellation/session behavior.

### P4-API-07: Go API Hygiene, Documentation, And Compatibility Staging

- Outcome: `fail`
- Affected public packages: `go-agent-loop/pkg/messages`, `go-agent-loop/pkg/agentloop`, `go-llm-gateway/pkg/models`, `go-llm-gateway/pkg/gateway`, `go-llm-gateway/pkg/providers`, `agent-cli/internal/agent`, `agent-cli/internal/services`
- Exported declarations: `messages.Message`, `messages.ContentPart`, `messages.StreamMessage`, `messages.ErrorValue`, `models.Message`, `models.SessionConfig`, `gateway.InferenceRequest`, `gateway.InteractionRequest`, `gateway.InteractionEvent`, `providers.Provider`, `providers.SessionProvider`, `providers.InferenceRequest`, `agent.Executor`, `agent.ProviderFactory`, `services.SessionRunOptions`
- Observable contract issue: core exported types mostly have comments and tests, but compatibility ownership is still uneven: `go-llm-gateway/pkg/models` aliases loop-owned message/session types under a gateway package path, `providers.InferenceRequest` and `gateway.InferenceRequest` overlap without a documented ownership boundary, and exported `internal/*` CLI composition types are stable inside the module but not downstream public API. Source inventories are useful evidence, but they are not product behavior unless the public contract itself is the inventory or documentation surface being reviewed.
- Implementation evidence:
  - `messages.Message`, `messages.ContentPart`, `messages.StreamMessage`, and `messages.ErrorValue` expose the shared cross-module message, stream, and typed-error vocabulary consumed by gateway, CLI, fixtures, and loop participants.
  - `go-llm-gateway/pkg/models` re-exports loop-owned `messages` and session declarations as aliases, so compatibility currently follows `go-agent-loop/pkg/messages` rather than an independent gateway model contract.
  - `gateway.InteractionRequest`, `gateway.InteractionEvent`, and PNIG payload types carry exported comments and deterministic JSON/event-shape tests.
  - `providers.Provider` and `providers.SessionProvider` stay narrow, but provider request/result ownership and overlap with gateway request/result aliases still need clearer package-level documentation.
- Planning-only evidence: DOC-01 remains open for the `pkg/models` alias facade; DOC-02 remains open for CLI `internal/*` composition boundaries; COMPAT-01, COMPAT-02, and COMPAT-03 remain open because message/session/error changes fan out into provider adapters, CLI output, replay/record fixtures, and text-matching callers.
- Docs/tests/examples evidence: `go doc ./go-agent-loop/pkg/messages`, `go doc ./go-llm-gateway/pkg/models`, `go doc ./go-llm-gateway/pkg/gateway`, and `go doc ./go-llm-gateway/pkg/providers` expose the current public comments; `go-llm-gateway/pkg/gateway/interaction_types_test.go` covers PNIG JSON/event shape stability; `go-llm-gateway/pkg/inference/*_test.go` covers gateway-to-loop adapter compatibility; session fixture validator tests cover persisted capture compatibility.
- Remaining repair slice: add package-level compatibility notes that declare `pkg/models` as an alias layer or migrate it to a gateway-owned vocabulary with adapters; document ownership between `gateway.InferenceRequest`, `providers.InferenceRequest`, and `models.*`; add internal development guidance that `agent-cli/internal/agent` and `agent-cli/internal/services` exports are application wiring, not downstream APIs; stage future shared message/session/error changes additively with fixture validators, CLI replay/record tests, and legacy text compatibility.
- Reviewer commands: from the repository root, run `go doc ./go-agent-loop/pkg/messages`, `go doc ./go-llm-gateway/pkg/gateway`, `go doc ./go-llm-gateway/pkg/models`, and `go doc ./go-llm-gateway/pkg/providers` to inspect public documentation; run `make typecheck` to verify exported contracts compile.

### P4-GATE-01: Phase 4 Closure Gate

- Outcome: `open`
- Affected public packages: all Phase 4 public surfaces named in P4-API-01 through P4-API-07
- Exported declarations: all exported declarations named in P4-API-01 through P4-API-07
- Observable contract issue: this documentation pass can identify and reconcile evidence, but it cannot close the implementation gate while API rows remain `fail` or `uncertain` and while dependent repair lanes have not consumed the reconciled findings.
- Implementation evidence: none sufficient for closure in this audit-only lane.
- Planning-only evidence: this reconciliation map and the detailed findings below identify the remaining repair slices.
- Docs/tests/examples evidence: reviewer commands in each row provide deterministic evidence for current state, not final closure.
- Remaining repair slice: complete typed errors/streams, provider capabilities/validation, dependency/result/context contracts, Go API hygiene, and final closure validation in the dedicated follow-up rows before closing P4-GATE-01.
- Reviewer commands: from the repository root, run `make typecheck` plus the row-specific commands above to verify current evidence; do not mark P4-GATE-01 closed from this audit document alone.

This document records the current contract gaps that Phase 2 needs to harden. It starts with the hidden-coupling and dependency-injection findings that block clean separation between the reusable libraries and the CLI composition layer.

Use [`dependencies.md`](./dependencies.md) for the intended dependency direction. Use this audit for the places where the codebase still relies on convenience coupling or constructor ownership that is broader than the intended architecture.

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
  - `go-llm-gateway/pkg/providers/openai/session.go` maps provider Realtime error payloads through `messages.NewErrorValueWithDetails(...)`, so current starter APIs are not absent for provider-supplied typed details
  - `go-agent-loop/pkg/participants/model_runner.go`, `go-agent-loop/pkg/participants/tool_runner.go`, `go-agent-loop/pkg/subsystems/interaction_events.go`, and multiple provider stream adapters still emit `messages.NewErrorValue(err.Error())` in observable error paths, dropping category and structured details
- Observable impact:
  - callers can detect that an error occurred and can read typed OpenAI Realtime details when those details are present, but they usually cannot distinguish retryable provider failures, invalid user input, transport shutdown, replay divergence, or tool runtime errors from the shared stream contract alone
  - review of downstream compatibility is harder because error handling currently depends on matching free-form message text rather than named failure classes
  - future changes to error wording could accidentally break callers or tests that only have the string payload to reason about
- Why this is a typed-error gap:
  - the contract has fields for structured classification, and selected OpenAI Realtime paths populate them, but most of the shared stream path still collapses failures into a plain message string before crossing module boundaries
- Recommended Phase 4 hardening:
  - define a small set of caller-actionable error classes at the shared contract layer
  - require adapters to preserve provider/runtime classification when turning internal failures into `ERROR` stream messages

### ERR-02: session command failures mix transport, replay, and loop phases into wrapped text instead of one caller-actionable taxonomy

- Affected boundary: `agent-cli/internal/services/session.go` user-facing command errors
- Evidence:
  - `agent-cli/internal/services/session_runtime.go` returns strings such as `replay session capture %s: %w`, and replay/runtime output paths can surface `session error: ...`
  - session tests prove `context.Canceled` is preserved through record and replay cancellation, but replay divergence, provider rejection, capture flush, and validation failures do not yet share stable exported categories
- Observable impact:
  - a caller or future automation layer cannot reliably separate validation failure, replay divergence, provider rejection, session-close reason, and capture-flush failure without parsing message text
  - the CLI can explain roughly where a failure happened, but not in a way that downstream contract hardening or machine classification can depend on
  - some failures are emitted in-band as `ERROR` stream messages, while others escape as Go errors, with no unified documented mapping between the two
- Why this is a typed-error gap:
  - the command surface preserves human-readable context and selected cancellation identity, but it does not yet expose stable error kinds that higher layers can branch on safely
- Recommended Phase 4 hardening:
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
  - `ErrorValue` already has type/code/detail fields, OpenAI Realtime uses them for provider error payloads, and PNIG interactions expose normalized `InteractionError` and `InteractionCancellation` payloads
  - many current loop, provider stream, interaction bridge, and CLI session paths still emit `err.Error()` strings or phase-prefixed wrapped errors
  - existing tests and operators can only infer meaning from message text on those remaining paths because error type/code fields are sparsely populated outside the current OpenAI Realtime and PNIG evidence
- Observable impact:
  - Phase 4 error normalization may require changing message text, adding codes, or moving some failures between returned Go errors and in-band stream errors
  - downstream automation that scrapes CLI stderr or recorded session events by substring may break unless the migration is staged carefully
- Recommended Phase 4 hardening:
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
