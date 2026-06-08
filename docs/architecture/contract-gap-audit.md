# Contract Gap Audit

This document records contract gaps that reviewers and later implementation
lanes can cite directly. It starts with the hidden-coupling and
dependency-injection findings that Phase 2 needed to harden, and it now also
defines the Phase 4 exported API audit shape for public contract hardening in
`go-agent-loop/pkg` and `go-llm-gateway/pkg`.

## Phase 4 API Audit Reconciliation Map

This section is the row-level Phase 4 source of truth for P4-API-01 through P4-API-07 and P4-GATE-01. The older findings below remain evidence and planning context, but audit documentation alone cannot close implementation checklist rows; a row is closable only when the named public contract, runtime behavior, docs/examples, and deterministic validation evidence exist in code or tests.

Status values:

- `pass`: current implementation and deterministic evidence are sufficient for this audit row.
- `fail`: current implementation evidence shows an observable contract gap.
- `uncertain`: evidence exists, but the row still needs targeted reconciliation before a reviewer should treat it as closed.
- `open`: intentionally not closable in this audit-only lane.

Reviewer command policy: run all commands from the repository root unless a row explicitly says otherwise. The test commands below use local Go packages, committed fixtures, and injected/mocked provider seams; they are intended to run without live provider credentials, external network access, or unspecified environment variables. `go doc` commands inspect the current public declarations and comments; they do not prove runtime behavior by themselves.

### Final Closure Validation Check

The Phase 4 validator final closure artifact is available at `docs/internal/phase-4-api-contract-validator.md`. Its final closure decision is that no reviewed Phase 4 checklist row may close from the current starter evidence and the next planner action is `repair`. This audit is therefore validated against current public declarations, README guidance, deterministic tests, and that validator report; `P4-GATE-01` remains open, and dependent repair lanes should consume this reconciled map before implementation or final gate review.

Cross-check summary:

| Row | Current audit decision | Closure validation result |
| --- | --- | --- |
| P4-API-01 | `fail` in this audit; validator outcome `uncertain` | Consistent with `go-agent-loop/README.md` constructor ownership guidance and `go-llm-gateway/README.md` transport ownership guidance: injected inferencer, tool executor, and HTTP runtime seams exist, but prompt/config/filesystem/dialer side effects plus full timeout/cancellation mapping still need exact repair work. |
| P4-API-02 | `fail` in this audit; validator outcome `uncertain` | Current starter evidence includes `messages.ErrorValue`, `messages.NewErrorValueWithDetails`, `gateway.GatewayError`, `ProviderHTTPStatusError`, public gateway error classes, PNIG errors, replay divergence, and cancellation evidence. Stale audit identifiers to keep reconciled are `P4-ERR-01`, `P4-ERR-02`, and `P4-ERR-03`; remaining gaps are provider/session/direct-stream coverage and serialized stream taxonomy. |
| P4-API-03 | `fail` in this audit; validator outcome `uncertain` | Consistent with current loop/gateway README behavior and tests: normalized stream/result evidence exists, but terminal authority remains split across `ExecuteResult`, synthesized stream endings, PNIG end/error/cancellation events, session `Done`/`Close`, replay completion, and CLI stop conditions. |
| P4-API-04 | `uncertain` | Current starter evidence now includes `go-llm-gateway/pkg/capabilities`, `providers.CapabilityReporter`, `gateway.CapabilityReporter`, `DefaultGateway.Capabilities`, `DefaultSessionGateway.Capabilities`, README guidance, and gateway capability tests. Stale audit wording to keep reconciled is `P4-CAP-01` language that says capability discovery is absent; remaining work is concrete provider coverage and avoiding overclaimed support. |
| P4-API-05 | `uncertain` | Stale or incomplete ownership wording remains isolated to `HC-02`, `DOC-01`, and stream taxonomy rows: `pkg/models` is visibly an alias facade in `go doc ./go-llm-gateway/pkg/models`, and stream/session error preservation still needs a documented mapping to gateway error classes. |
| P4-API-06 | `fail` in this audit; validator outcome `uncertain` | Current starter evidence includes `validateStatelessRequest`, `validateSessionConfig`, and structured `UnsupportedFeatureError` for explicit unsupported gateway/session capabilities. Stale audit wording to keep reconciled is `P4-VALIDATION-01` language that says unsupported-feature validation is absent; remaining work is interaction/inferencer coverage, concrete provider reporting, and fal streaming behavior. |
| P4-API-07 | `fail` in this audit; validator outcome `uncertain` | Consistent with public declaration inspection: comments and exported declarations are inspectable, but compatibility staging for `pkg/models`, overlapping gateway/provider request types, CLI internal composition exports, fixtures, hidden side effects, and text-matching callers remains open. |
| P4-GATE-01 | `open`; validator outcome `fail` | Not closable from this audit. Rows with `fail` or `uncertain` outcomes plus the validator's `must remain open` decisions block final closure. |

Validated public declaration commands:

- `go doc ./go-agent-loop/pkg/messages` prints shared message, stream, session, and typed-error declarations including `ErrorValue`, `Session`, `SessionInferencer`, and `StreamMessage`.
- `go doc ./go-llm-gateway/pkg/gateway` prints stateless, session, PNIG interaction, capability, validation, fixture, and typed-error declarations including `DefaultGateway`, `DefaultSessionGateway`, `CapabilityReporter`, `UnsupportedFeatureError`, `GatewayError`, `ProviderHTTPStatusError`, `InteractionEvent`, `InteractionError`, and request/response aliases.
- `go doc ./go-llm-gateway/pkg/models` prints the current alias facade over loop-owned message types plus gateway-owned session config/event declarations.
- `go doc ./go-llm-gateway/pkg/providers` prints `Provider`, `SessionProvider`, `CapabilityReporter`, `ProviderCapabilities`, `UnsupportedFeatureError`, `UnknownProviderCapabilities`, `InferenceRequest`, `InferenceResponse`, `ThinkingConfig`, and `CacheControlConfig`.

Validated deterministic test names and packages:

- `go-agent-loop/pkg/agentloop/agent_loop_test.go` covers explicit tool-execution constructor decisions.
- `go-llm-gateway/pkg/providers/openai/session_test.go` covers OpenAI Realtime typed error detail preservation.
- `go-llm-gateway/pkg/gateway/interaction_gateway_test.go` covers PNIG provider errors, timeouts, cancellation, and terminal events.
- `go-llm-gateway/pkg/gateway/errors_test.go`, `gateway_test.go`, and `capabilities_test.go` cover public gateway typed errors, capability discovery, unsupported-feature validation, and unknown fallback behavior.
- `go-llm-gateway/pkg/testing/session_replay_test.go` covers replay divergence, omitted outbound events, and replay cancellation.
- `agent-cli/internal/services/session_test.go` covers record/replay cancellation preservation and session command error behavior.
- `agent-cli/internal/config/models_test.go`, `agent-cli/internal/input/validate_test.go`, and `agent-cli/internal/agent/provider_runtime_test.go` cover model metadata, unsupported input/output validation, and provider runtime ownership.
- `go-llm-gateway/pkg/providers/openai/capabilities_test.go`, `go-llm-gateway/pkg/providers/anthropic/params_test.go`, `go-llm-gateway/pkg/providers/gemini/replay_test.go`, `go-llm-gateway/pkg/providers/gemini/stream_test.go`, and `go-llm-gateway/pkg/providers/fal/provider_test.go` cover provider-local capability, option, modality, stream, and media/config behavior without live credentials.

### Credential-Free Reviewer Command Matrix

| Command | Rows | Evidence claim | Expected pass condition |
| --- | --- | --- | --- |
| `make typecheck` | P4-API-01, P4-API-07, P4-GATE-01 | Workspace public packages and the CLI binary compile under the current `go.work` module graph. | The root build target exits successfully for `agent-cli`, `go-agent-loop`, and `go-llm-gateway`. |
| `make test` | P4-API-02, P4-API-03, P4-API-04, P4-API-06 | Deterministic unit and package tests for loop, gateway, provider, replay, CLI service, and fixture behavior pass without live credentials. | All module-local `go test ./... -timeout 120s` invocations exit successfully. |
| `(cd go-agent-loop && go test ./pkg/agentloop)` | P4-API-01 | Agent loop constructor ownership for injected inferencers and tool execution remains deterministic. | The package test exits successfully. |
| `(cd agent-cli && go test ./internal/agent ./internal/services)` | P4-API-01, P4-API-02, P4-API-06 | CLI-owned provider runtime seams, session runtime planning, replay/record cancellation, and command error behavior remain covered by local tests. | Both package tests exit successfully without provider credentials. |
| `(cd go-llm-gateway && go test ./pkg/gateway ./pkg/testing ./pkg/providers/openai ./pkg/providers/anthropic ./pkg/providers/gemini ./pkg/providers/grok)` | P4-API-02, P4-API-06 | PNIG typed errors, replay divergence, replay cancellation, provider stream behavior, and credential-free session construction evidence remain deterministic. | All listed gateway/provider package tests exit successfully. |
| `(cd go-agent-loop && go test ./pkg/participants ./pkg/subsystems ./test/functional)` | P4-API-02, P4-API-03, P4-API-06 | Loop participants, subsystems, and functional session flows preserve stream, error, lifecycle, and cancellation behavior. | All listed loop package tests exit successfully. |
| `(cd go-agent-loop && go test ./pkg/engine ./test/functional)` | P4-API-03 | Loop result reconstruction and session lifecycle behavior remain deterministic. | Both package tests exit successfully. |
| `(cd agent-cli && go test ./internal/config ./internal/input ./internal/agent)` | P4-API-04 | CLI model metadata, MIME validation, and executor-side unsupported-feature validation behavior remain local and credential-free. | All listed CLI package tests exit successfully. |
| `(cd go-llm-gateway && go test ./pkg/providers/openai ./pkg/providers/anthropic ./pkg/providers/gemini ./pkg/providers/fal ./pkg/providers/grok)` | P4-API-04 | Provider-local option mapping, modality handling, stream behavior, session construction, and provider-specific config passthrough are covered by deterministic tests. | All listed provider package tests exit successfully without provider credentials. |
| `(cd go-llm-gateway && go test ./pkg/inference ./pkg/gateway)` | P4-API-05 | Gateway, PNIG, and loop inference adapter contracts still agree on current request, response, and event shapes. | Both package tests exit successfully. |
| `(cd go-llm-gateway && go test ./pkg/testing ./pkg/gateway)` | P4-API-06 | Replay/record lifecycle behavior and PNIG cancellation/timeout event evidence remain deterministic. | Both package tests exit successfully. |
| `(cd go-agent-loop && go test ./test/functional -run 'TestRun_ExitsOnContextCancellation|TestSession')` | P4-API-06 | Loop cancellation and session behavior relevant to context ownership remain covered by targeted functional tests. | The targeted functional tests exit successfully. |
| `go doc ./go-agent-loop/pkg/messages` | P4-API-02, P4-API-06, P4-API-07 | Current shared message, stream, session, and typed error declarations are inspectable as public documentation. | `go doc` prints package declarations without error. |
| `go doc ./go-llm-gateway/pkg/gateway` | P4-API-03, P4-API-05, P4-API-07 | Current gateway stateless, session, and PNIG contracts are inspectable as public documentation. | `go doc` prints package declarations without error. |
| `go doc ./go-llm-gateway/pkg/models` | P4-API-05, P4-API-07 | Current gateway model alias facade is inspectable and can be compared against `go-agent-loop/pkg/messages`. | `go doc` prints package declarations without error. |
| `go doc ./go-llm-gateway/pkg/providers` | P4-API-04, P4-API-07 | Current provider declarations are inspectable, including the provider-neutral `CapabilityReporter`, `ProviderCapabilities`, and `UnsupportedFeatureError` aliases plus remaining provider request/session declarations. | `go doc` prints package declarations without error. |

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

- Outcome: `uncertain`
- Affected public packages: `agent-cli/internal/config`, `agent-cli/internal/input`, `agent-cli/internal/agent`, `go-llm-gateway/pkg/providers`, `go-llm-gateway/pkg/providers/openai`, `go-llm-gateway/pkg/providers/anthropic`, `go-llm-gateway/pkg/providers/gemini`, `go-llm-gateway/pkg/providers/fal`, `go-llm-gateway/pkg/providers/grok`
- Exported declarations: `config.ModelInfo`, `(*config.ModelInfo).SupportsOutputModality`, `(*config.ModelInfo).SupportsInputMimeType`, `config.ModelsConfig.Lookup`, `input.ValidateMimeType`, `input.ValidateContentPartsMimeTypes`, `agent.Executor.RunAsk`, `providers.Provider`, `providers.SessionProvider`, `providers.CapabilityReporter`, `providers.ProviderCapabilities`, `providers.UnknownProviderCapabilities`, `providers.UnsupportedFeatureError`, `gateway.CapabilityReporter`, `gateway.DefaultGateway.Capabilities`, `gateway.DefaultSessionGateway.Capabilities`, `gateway.UnsupportedFeatureError`, `providers.InferenceRequest`, `providers.ThinkingConfig`, `providers.CacheControlConfig`
- Observable contract issue: current implementation now has a provider-neutral capability model, gateway/session discovery methods, structured unsupported-feature validation for explicitly unsupported gateway/session capabilities, CLI-local model metadata, and selected provider-specific request options. The row remains uncertain because concrete provider capability reporters are incomplete across provider families, unknown capabilities intentionally fall through, interaction/inferencer seams still need closure decisions, fal streaming behavior needs alignment with the public capability contract, and older `P4-CAP-01` / `P4-VALIDATION-01` wording can become stale when it says discovery or validation is absent rather than partially implemented.
- Implementation evidence:
  - Supported locally: `config.ModelInfo` records `InputModalities`, `OutputModalities`, `SupportsToolUse`, `SupportsReasoning`, tokenizer, provider IDs, aliases, and `SupportedInputMimeTypes`; `SupportsOutputModality`, `SupportsInputMimeType`, and `ModelsConfig.Lookup` provide deterministic metadata checks.
  - Unsupported locally: `agent.Executor.RunAsk` calls `validateOutputModality` and `validateInputMimeTypes`, which reject non-text output modalities and input MIME types when the selected OpenAI-compatible model is known in `models.yaml`.
  - Unknown locally: CLI validation skips enforcement when model config cannot be loaded, the active provider is not OpenAI-compatible, `runData.Models` is absent, or the model is missing from `ModelsConfig`; those paths deliberately let the provider decide.
  - Provider-neutral starter evidence: `go-llm-gateway/pkg/capabilities.ProviderCapabilities` represents supported, unsupported, and unknown feature states; `providers.CapabilityReporter` and `gateway.CapabilityReporter` expose discovery; `DefaultGateway.Capabilities` and `DefaultSessionGateway.Capabilities` return provider-reported capabilities or the documented unknown fallback; `validateStatelessRequest` and `validateSessionConfig` return `UnsupportedFeatureError` before provider execution when a requested feature is explicitly unsupported.
  - Provider-specific evidence: `providers.InferenceRequest.Tools` is accepted by stateless providers that implement tool translation; `Provider.InferStream` is the only streaming affordance on the provider interface; `SessionProvider.ConnectSession` is the only session capability seam; `ThinkingConfig` is mapped by Anthropic and ignored by OpenAI; `CacheControlConfig` is mapped by Anthropic while OpenAI keeps default cache behavior; `InferenceRequest.Config` is passed through by fal.ai for model-specific options.
  - Capability status by requested feature: tools, streaming, sessions, audio, image input, video output, reasoning, prompt caching, and provider-specific config now have a provider-neutral state vocabulary, but provider-specific support remains mixed between explicitly supported, explicitly unsupported, and unknown depending on provider coverage.
- Planning-only evidence: P4-CAP-01 is reconciled from stale "capability discovery absent" wording to "provider-neutral discovery exists, but concrete provider coverage and public closure evidence remain incomplete"; P4-VALIDATION-01 is reconciled from stale "unsupported-feature validation absent" wording to "gateway/session unsupported-feature validation exists for explicitly unsupported capabilities, while interaction/inferencer seams, concrete provider reporting, and selected provider-local behaviors remain open."
- Docs/tests/examples evidence:
  - `agent-cli/internal/config/models_test.go` verifies output modality checks, input MIME metadata, default multimodal MIME coverage, lookup behavior, and unknown-model allow behavior.
  - `agent-cli/internal/input/validate_test.go` verifies MIME rejection errors, supported-list behavior, conversion hints, and validation across image, audio, video, and file parts.
  - `agent-cli/internal/agent/executor.go` contains the observable validation flow through `RunAsk`, `validateOutputModality`, and `validateInputMimeTypes`.
  - `go-llm-gateway/pkg/capabilities/capabilities_test.go` verifies unknown provider capability fallback and JSON round-tripping.
  - `go-llm-gateway/pkg/gateway/capabilities_test.go` verifies gateway/session discovery, unsupported-feature rejection before provider execution or connection, and unknown fallback behavior.
  - `go-llm-gateway/pkg/providers/openai/params_test.go` verifies OpenAI option mapping and that `Thinking` is ignored.
  - `go-llm-gateway/pkg/providers/openai/capabilities_test.go` verifies OpenAI concrete capability reporting.
  - `go-llm-gateway/pkg/providers/anthropic/params_test.go` verifies `ThinkingConfig` and `CacheControlConfig` mapping.
  - `go-llm-gateway/pkg/providers/gemini/replay_test.go`, `go-llm-gateway/pkg/providers/gemini/stream_test.go`, and `go-llm-gateway/pkg/providers/gemini/models_test.go` verify deterministic Gemini tool, image/audio content, and streaming behavior.
  - `go-llm-gateway/pkg/providers/fal/provider_test.go` verifies audio-to-video, image-to-video, TTS, unsupported model errors, and `Config` passthrough without live provider credentials.
  - `go-llm-gateway/pkg/providers/grok/provider_test.go` and `go-llm-gateway/pkg/providers/openai/session_test.go` verify credential-free session construction behavior through injected dialers.
- Remaining repair slice: keep P4-CAP-01 open to complete or verify concrete provider capability reporters and credential-free tests across Anthropic, Gemini, fal.ai, Grok/session, OpenAI-compatible variants, and any other provider family where current behavior still falls back to unknown; keep P4-VALIDATION-01 open to extend or document validation through interaction gateways and `GatewayInferencer`, settle fal streaming behavior against the public capability contract, prove representative unsupported features fail locally before provider side effects, and document when unknown capabilities are intentionally deferred to the provider.
- Reviewer commands: from the repository root, run `(cd agent-cli && go test ./internal/config ./internal/input ./internal/agent)` to verify CLI-local model metadata, MIME validation, and executor validation behavior; run `(cd go-llm-gateway && go test ./pkg/capabilities ./pkg/gateway ./pkg/providers/openai ./pkg/providers/anthropic ./pkg/providers/gemini ./pkg/providers/fal ./pkg/providers/grok)` to verify provider-neutral capability, gateway validation, provider-local deterministic option, modality, stream, session, and provider-specific config behavior; run `go doc ./go-llm-gateway/pkg/providers` and `go doc ./go-llm-gateway/pkg/gateway` to inspect current capability and unsupported-feature declarations.

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

### Phase 4 Typed Error And Stream Semantics Findings

These findings cover the `P4-API-02` and `P4-API-05` story slice. They name
public error and stream surfaces where callers cannot yet branch on stable
failure classes or rely on one provider-neutral terminal event contract.

#### P4-ERR-01: provider status and transport errors are not represented by a shared typed error taxonomy

- Affected package: `github.com/portpowered/go-llm-gateway/pkg/providers`,
  `github.com/portpowered/go-llm-gateway/pkg/gateway`,
  `github.com/portpowered/go-llm-gateway/pkg/inference`
- File path: `go-llm-gateway/pkg/providers/provider.go`,
  `go-llm-gateway/pkg/providers/openai/provider.go`,
  `go-llm-gateway/pkg/providers/anthropic/provider.go`,
  `go-llm-gateway/pkg/providers/gemini/provider.go`,
  `go-llm-gateway/pkg/providers/fal/provider.go`,
  `go-llm-gateway/pkg/gateway/gateway.go`,
  `go-llm-gateway/pkg/inference/main_inferencer.go`
- Exported declaration: `Provider.Infer`, `Provider.InferStream`,
  `Gateway.Infer`, `Gateway.InferStream`, `GatewayInferencer.Infer`,
  `GatewayInferencer.InferStream`
- Observable contract issue:
  - OpenAI and fal HTTP errors are returned as formatted strings containing
    status code and response body, while Anthropic and Gemini SDK errors are
    passed through directly. Callers cannot use `errors.Is` or `errors.As` to
    branch uniformly on auth, authorization, rate limit, invalid request,
    unsupported model, provider status, transport failure, cancellation, or
    deadline exceeded.
  - Gateway and inference adapters forward those provider errors without adding
    a shared error kind, so downstream loop callers inherit provider-specific
    error text as the public contract.
  - Current replay tests assert status-code substrings such as `400`, `401`,
    `429`, and `500`, which demonstrates that caller-observable behavior is
    string parsing rather than a stable typed error surface.
- Mapped checklist rows: `P4-API-02`, `P4-API-03`, `P4-API-04`
- Severity: `must-fix contract defect`
- Compatibility sensitivity: `additive`
- Recommended repair slice:
  - add provider-neutral exported error kinds, for example
    `ErrInvalidRequest`, `ErrAuthentication`, `ErrAuthorization`,
    `ErrRateLimited`, `ErrUnsupportedModel`, `ErrProviderStatus`,
    `ErrTransport`, and wrappers that expose provider name, status code,
    request ID, retryability, and provider body/code fields
  - require all provider implementations to wrap local validation, HTTP status,
    SDK, transport, cancellation, and deadline failures so
    `errors.Is`/`errors.As` works through `Gateway` and `GatewayInferencer`
  - keep existing error messages as human-readable text where practical, but
    move compatibility-sensitive assertions from substring checks to typed
    error checks in later implementation lanes
- Verification notes:
  - later repair lanes should add provider-neutral gateway tests plus
    provider-specific replay tests for 400, 401, 403, 404 or unsupported model,
    429, 5xx, transport errors, `context.Canceled`, and
    `context.DeadlineExceeded`

#### P4-ERR-02: stream error events carry free-form messages instead of caller-actionable error classes

- Affected package: `github.com/portpowered/go-agent-loop/pkg/messages`,
  `github.com/portpowered/go-agent-loop/pkg/participants`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/openai`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/anthropic`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/gemini`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/grok`
- File path: `go-agent-loop/pkg/messages/agent_messages.go`,
  `go-agent-loop/pkg/participants/model_runner.go`,
  `go-agent-loop/pkg/participants/tool_runner.go`,
  `go-llm-gateway/pkg/providers/openai/stream.go`,
  `go-llm-gateway/pkg/providers/openai/session.go`,
  `go-llm-gateway/pkg/providers/anthropic/stream.go`,
  `go-llm-gateway/pkg/providers/gemini/stream.go`,
  `go-llm-gateway/pkg/providers/grok/session.go`
- Exported declaration: `ErrorValue`,
  `NewErrorValue`, `NewErrorValueWithDetails`,
  `Inferencer.InferStream`, `Provider.InferStream`,
  `messages.StreamMessage` with `StreamTypeError`
- Observable contract issue:
  - `ErrorValue` has optional provider detail fields, but most public stream
    paths still emit `NewErrorValue(err.Error())`. This collapses transport
    failures, provider status failures, invalid request failures, tool runtime
    failures, parser failures, replay divergence, and cancellation into one
    free-form string.
  - OpenAI realtime provider errors preserve provider `error.type`, `code`,
    `param`, and `event_id`, while HTTP streaming, Anthropic, Gemini, tool
    runner, and model runner errors do not expose the same caller-actionable
    fields or retryability rule.
  - The public stream contract has no exported sentinel or structured kind that
    maps an in-band `ERROR` event back to the returned Go error taxonomy.
- Mapped checklist rows: `P4-API-02`, `P4-API-05`, `P4-API-07`
- Severity: `must-fix contract defect`
- Compatibility sensitivity: `additive`
- Recommended repair slice:
  - extend `ErrorValue` additively with stable error kind and retryability
    fields, or define an exported stream error payload type that can carry the
    same provider-neutral classes as returned Go errors
  - update stream adapters and participants to preserve typed classes when
    converting returned errors into `StreamTypeError` events
  - document how callers should compare returned errors and in-band stream
    error events without parsing provider or tool error text
- Verification notes:
  - later repair lanes should add stream tests that assert error kind and
    retryability for provider stream status errors, stream parser errors,
    cancelled streams, tool executor failures, and OpenAI realtime provider
    error events

#### P4-ERR-03: replay mismatch and fixture validation errors are public but not integrated into the shared error model

- Affected package: `github.com/portpowered/go-llm-gateway/pkg/testing`,
  `github.com/portpowered/go-llm-gateway/pkg/gateway`
- File path: `go-llm-gateway/pkg/testing/session_replay.go`,
  `go-llm-gateway/pkg/testing/session_websocket_dialer.go`,
  `go-llm-gateway/pkg/gateway/interaction_fixture.go`
- Exported declaration: `SessionReplayer.Err`, `ReplayWebSocketDialer.Err`,
  `InteractionFixtureValidationError`,
  `ValidateInteractionFixture`, `DecodeInteractionFixture`
- Observable contract issue:
  - session replay divergence, replay incompletion, unexpected outbound event,
    and unsupported payload failures are returned as formatted strings. There
    is no exported replay mismatch type carrying sequence, direction, expected
    event type, actual event type, and fixture path.
  - interaction fixture validation does expose
    `InteractionFixtureValidationError`, but the replay/session fixture
    surfaces do not share that typed validation contract, so tests and
    automation must parse unrelated strings to identify fixture authoring
    failures versus runtime provider failures.
  - replay mismatch can appear through `Send` returning `false`, `Close`
    returning `nil`, `Err()`, or WebSocket write/read errors depending on the
    replay wrapper, without a shared typed error that callers can inspect.
- Mapped checklist rows: `P4-API-02`, `P4-API-03`, `P4-API-05`
- Severity: `must-fix contract defect`
- Compatibility sensitivity: `additive`
- Recommended repair slice:
  - introduce exported replay and fixture error types with `errors.As` support,
    including sequence, path, expected direction/type, actual direction/type,
    and mismatch kind
  - thread those typed errors through `SessionReplayer.Err`,
    `ReplayWebSocketDialer.Err`, replay `Send`/`Close` paths, fixture decoding,
    and any in-band stream error events produced from replay failure
  - keep current messages as `Error()` text, but make test assertions and
    caller examples use typed mismatch fields
- Verification notes:
  - later repair lanes should add tests for unexpected outbound event, payload
    mismatch, incomplete replay on close, unsupported payload type, and fixture
    validation using `errors.As`

#### P4-STREAM-01: streaming APIs do not expose one terminal error and final-event contract

- Affected package: `github.com/portpowered/go-llm-gateway/pkg/providers`,
  `github.com/portpowered/go-llm-gateway/pkg/gateway`,
  `github.com/portpowered/go-agent-loop/pkg/messages`,
  `github.com/portpowered/go-agent-loop/pkg/agentloop`
- File path: `go-llm-gateway/pkg/providers/provider.go`,
  `go-llm-gateway/pkg/gateway/interfaces.go`,
  `go-agent-loop/pkg/messages/participant_messages.go`,
  `go-agent-loop/pkg/agentloop/execute_result.go`
- Exported declaration: `Provider.InferStream`, `Gateway.InferStream`,
  `Inferencer.InferStream`, `AgenticLoop.ExecuteStreaming`,
  `Stream.HasNext`, `Stream.Err`, `StreamingExecuteResult.Messages`
- Observable contract issue:
  - `InferStream(ctx, req)` returns only a receive channel after initial setup.
    Provider failures that occur after the channel is returned must be encoded
    as events or disappear behind channel close; there is no stream handle with
    a final error result.
  - `Stream.Err()` exists at the loop layer, but provider stream channels and
    gateway streams do not expose a corresponding final error. As a result,
    callers cannot apply one rule across direct provider use, gateway use, and
    loop streaming use.
  - The exported contract does not state whether a stream error is followed by
    `MESSAGE.END`, whether `USAGE.INFO` may appear after `MESSAGE.END`, whether
    cancellation emits an error event or only closes the channel, or whether
    `Messages()` includes partial messages after a stream failure.
- Mapped checklist rows: `P4-API-01`, `P4-API-02`, `P4-API-03`,
  `P4-API-05`
- Severity: `must-fix contract defect`
- Compatibility sensitivity: `compatibility-sensitive`
- Recommended repair slice:
  - define a provider-neutral stream lifecycle: setup error, event sequence,
    terminal error event, final message/end event, usage event placement,
    channel close, and final error accessor
  - add an additive stream result handle or terminal status event to gateway and
    provider APIs, then have `AgenticLoop.ExecuteStreaming` preserve the same
    final status in `Stream.Err()` and `Messages()`
  - document whether partial messages are caller-visible on late provider
    failure and how callers should drain or close streams
- Verification notes:
  - later repair lanes should add fake-provider tests proving late stream
    failure, cancellation, clean completion, usage placement, and partial
    message preservation through provider, gateway, inference bridge, and
    `AgenticLoop.ExecuteStreaming`

#### P4-STREAM-02: provider stream adapters disagree on event ordering around errors, message end, usage, and empty streams

- Affected package: `github.com/portpowered/go-llm-gateway/pkg/providers/openai`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/anthropic`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/gemini`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/fal`
- File path: `go-llm-gateway/pkg/providers/openai/stream.go`,
  `go-llm-gateway/pkg/providers/anthropic/stream.go`,
  `go-llm-gateway/pkg/providers/gemini/stream.go`,
  `go-llm-gateway/pkg/providers/fal/provider.go`
- Exported declaration: `OpenAIProvider.InferStream`,
  `AnthropicProvider.InferStream`, `GeminiProvider.InferStream`,
  `Provider.InferStream` as implemented by fal
- Observable contract issue:
  - OpenAI emits `MESSAGE.END` before scanner errors discovered after the SSE
    loop, while Anthropic and Gemini emit `ERROR` before `MESSAGE.END` for
    stream iterator errors. A caller cannot rely on a single rule for whether
    `ERROR` is terminal or whether `MESSAGE.END` means success.
  - Usage placement is provider-specific: OpenAI and Anthropic can emit
    `USAGE.INFO` after `MESSAGE.END`, Gemini emits it after `MESSAGE.END` when
    usage is present, and fal returns an immediately closed stream with no
    start/end/error events because it has no streaming implementation.
  - Empty or setup-only streams are not classified: Gemini forces
    `MESSAGE.START`/`MESSAGE.END` even when no content arrives, fal returns a
    closed channel with no events, and OpenAI may emit start/end around scanner
    EOF. These are all public channel contracts.
- Mapped checklist rows: `P4-API-02`, `P4-API-04`, `P4-API-05`
- Severity: `must-fix contract defect`
- Compatibility sensitivity: `compatibility-sensitive`
- Recommended repair slice:
  - standardize stream event ordering for all providers, including
    `MESSAGE.START`, content start/delta/end pairs, tool-call pairs,
    `StreamTypeError`, `MESSAGE.END`, `USAGE.INFO`, and channel close
  - define whether unsupported streaming should fail synchronously with
    `ErrUnsupportedCapability` or return a typed terminal stream error instead
    of a clean empty stream
  - update provider adapter tests to assert the same final event contract across
    OpenAI, Anthropic, Gemini, and fal
- Verification notes:
  - later repair lanes should add table-driven provider stream tests for clean
    text, tool call, parser error, empty stream, unsupported streaming, and
    usage-bearing completion

#### P4-STREAM-03: session stream close and cancellation events lack a shared final status

- Affected package: `github.com/portpowered/go-agent-loop/pkg/messages`,
  `github.com/portpowered/go-agent-loop/pkg/participants`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/openai`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/grok`,
  `github.com/portpowered/go-llm-gateway/pkg/testing`
- File path: `go-agent-loop/pkg/messages/session.go`,
  `go-agent-loop/pkg/messages/agent_messages.go`,
  `go-agent-loop/pkg/participants/model_runner.go`,
  `go-llm-gateway/pkg/providers/openai/session.go`,
  `go-llm-gateway/pkg/providers/grok/session.go`,
  `go-llm-gateway/pkg/testing/session_replay.go`,
  `go-llm-gateway/pkg/testing/session_record.go`
- Exported declaration: `Session`, `Session.Done`, `Session.Close`,
  `SessionCloseValue`, `ErrorValue`, `SessionReplayer.Err`,
  `RecordingSessionInferencer.ConnectSession`, `ReplaySessionInferencer.ConnectSession`
- Observable contract issue:
  - session close, provider close, caller close, cancellation, transport error,
    provider `error` event, replay divergence, and replay completion are
    observable through different combinations of `SESSION.CLOSE`,
    `StreamTypeError`, `Done()`, `Close() error`, and wrapper-specific `Err()`.
  - `ModelRunner` synthesizes `SESSION.CLOSE` with reason `provider_closed`
    when `session.Done()` closes without an explicit close event, but the
    shared `Session` contract does not require providers, recorders, or
    replayers to expose a final status reason.
  - `SessionCloseValue.Reason` is a string, not a typed enum or error class, so
    callers cannot distinguish normal provider close, caller cancellation,
    timeout, replay mismatch, or transport shutdown without provider-specific
    string conventions.
- Mapped checklist rows: `P4-API-01`, `P4-API-02`, `P4-API-03`,
  `P4-API-05`
- Severity: `must-fix contract defect`
- Compatibility sensitivity: `compatibility-sensitive`
- Recommended repair slice:
  - define exported session terminal status kinds and map them to
    `SESSION.CLOSE`, `ERROR`, `Done()`, `Close()`, and optional `Err()` accessors
  - update OpenAI, Grok, recorder, and replayer sessions to preserve the same
    terminal reason for provider close, caller close, cancellation, timeout,
    transport failure, provider error, replay divergence, and replay completion
  - keep existing reason strings as compatibility text while adding typed
    status fields or accessors for new callers
- Verification notes:
  - later repair lanes should add shared session contract tests plus provider
    session tests for provider close event, caller close, context cancellation,
    transport read/write failure, provider error event, replay divergence, and
    replay completion

#### Typed Error And Stream Repair Slice Order

1. Define provider-neutral returned Go error kinds first so direct `Infer` and
   `InferStream` setup failures have a stable taxonomy before stream events
   mirror it.
2. Extend `ErrorValue` or add a stream error payload that carries the same
   taxonomy, then update provider stream adapters and participants to preserve
   typed classes in-band.
3. Standardize provider stream terminal ordering and unsupported-streaming
   behavior before changing loop-level `Stream.Err()` semantics.
4. Add replay and fixture mismatch error types so record/replay evidence can
   test stream and session failures without string parsing.
5. Define session terminal status kinds and align live, recording, and replay
   sessions after the shared returned-error and stream-error taxonomy exists.

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
make test
```

No reproducibility tooling was added for this slice; later implementation lanes
must add the tests named in each finding before closing any Phase 4 checklist
row.

### Phase 4 Provider Capability, Validation, And Dependency Injection Findings

These findings cover the `P4-API-04`, `P4-API-06`, and `P4-API-07` story
slice. They name gateway/provider seams where callers can set feature-bearing
request fields or runtime dependencies, but cannot discover support, validate
unsupported requests locally, or consistently inject the dependencies needed to
own test, replay, timeout, and transport behavior.

#### P4-CAP-01: provider support for tools, media, reasoning, caching, sessions, and streaming is not discoverable

- Affected package: `github.com/portpowered/go-llm-gateway/pkg/providers`,
  `github.com/portpowered/go-llm-gateway/pkg/gateway`,
  `github.com/portpowered/go-llm-gateway/pkg/models`
- File path: `go-llm-gateway/pkg/providers/provider.go`,
  `go-llm-gateway/pkg/providers/session_provider.go`,
  `go-llm-gateway/pkg/gateway/interfaces.go`,
  `go-llm-gateway/pkg/gateway/session_gateway.go`,
  `go-llm-gateway/pkg/models/message.go`,
  `go-llm-gateway/pkg/models/session.go`
- Exported declaration: `Provider`, `SessionProvider`, `InferenceRequest`,
  `Gateway`, `DefaultGateway`, `DefaultSessionGateway`, `models.Message`,
  `models.ContentPart`, `models.ToolDefinition`, `models.SessionConfig`,
  `providers.ThinkingConfig`, `providers.CacheControlConfig`
- Observable contract issue:
  - `InferenceRequest` exposes `Tools`, `Model`, `Thinking`, `CacheControl`,
    `Config`, and gateway model aliases expose text, image, audio, video,
    embedding, reasoning, tool, and session shapes, but `Provider` only exposes
    `Name`, `Infer`, and `InferStream`. A caller cannot ask whether the selected
    provider supports tools, streaming, bidirectional sessions, audio input or
    output, image input, video output, reasoning, prompt caching, embeddings, or
    provider-specific config before sending the request.
  - Session support is discoverable only by selecting a separate
    `SessionProvider` at composition time. There is no common capability value
    that tells a gateway consumer whether the same provider family supports
    stateless inference, streaming inference, or realtime sessions for a model.
  - `ThinkingConfig` and `CacheControlConfig` comments name provider-specific
    behavior, but unsupported providers can ignore or pass through those fields
    without a stable public unsupported-capability result.
- Mapped checklist rows: `P4-API-04`, `P4-API-06`, `P4-API-07`,
  `P4-API-02`, `P4-API-03`
- Severity: `must-fix contract defect`
- Compatibility sensitivity: `additive`
- Recommended repair slice:
  - add a provider-neutral exported capability model, for example
    `ProviderCapabilities` plus optional `Capabilities(ctx)` or
    `CapabilitiesForModel(ctx, model)` methods, covering tools, streaming,
    sessions, audio input/output, image input, video output, reasoning, prompt
    caching, embeddings, provider config, and provider-specific limits
  - expose a gateway-level capability discovery method so consumers do not need
    to type-switch concrete providers or know whether a session provider is
    separately wired
  - document the rule that absence of a capability must be observable before
    provider execution through local validation or a typed unsupported
    capability error
- Verification notes:
  - later repair lanes should add fake-provider and concrete-provider tests
    proving capability discovery for OpenAI, Anthropic, Gemini, fal, Grok
    realtime sessions, and gateway wrappers without issuing live network calls

#### P4-VALIDATION-01: unsupported request features fail at provider execution instead of a shared local validation seam

- Affected package: `github.com/portpowered/go-llm-gateway/pkg/gateway`,
  `github.com/portpowered/go-llm-gateway/pkg/providers`,
  `github.com/portpowered/go-llm-gateway/pkg/inference`
- File path: `go-llm-gateway/pkg/gateway/gateway.go`,
  `go-llm-gateway/pkg/gateway/interfaces.go`,
  `go-llm-gateway/pkg/providers/provider.go`,
  `go-llm-gateway/pkg/providers/fal/provider.go`,
  `go-llm-gateway/pkg/providers/anthropic/params.go`,
  `go-llm-gateway/pkg/providers/openai/params.go`,
  `go-llm-gateway/pkg/providers/gemini/provider.go`,
  `go-llm-gateway/pkg/inference/main_inferencer.go`
- Exported declaration: `DefaultGateway.Infer`,
  `DefaultGateway.InferStream`, `Provider.Infer`, `Provider.InferStream`,
  `providers.InferenceRequest`, `gateway.InferenceRequest`,
  `GatewayInferencer.Infer`, `GatewayInferencer.InferStream`
- Observable contract issue:
  - `DefaultGateway` forwards all request fields directly to the provider with
    no exported validation step. Unsupported tools, streaming, media parts,
    reasoning, prompt caching, config blobs, or missing model choices therefore
    fail differently per provider: some are ignored, some become formatted
    provider errors, some become local string errors, and fal streaming returns
    an immediately closed channel even though streaming is unsupported.
  - fal validates model-specific content requirements inside `Infer` and returns
    string errors such as missing audio/image/embedding input or unsupported
    model. OpenAI, Anthropic, and Gemini translate broad request shapes into
    provider-specific params without a shared unsupported-field taxonomy. The
    gateway consumer cannot distinguish "unsupported by this provider" from
    invalid user input or provider runtime failure without parsing text.
  - `GatewayInferencer` forwards only the loop request plus its configured model
    and raw JSON config; it has no validation hook to reject an unsupported
    loop request before the agent loop observes provider execution behavior.
- Mapped checklist rows: `P4-API-04`, `P4-API-06`, `P4-API-07`,
  `P4-API-02`, `P4-API-03`
- Severity: `must-fix contract defect`
- Compatibility sensitivity: `compatibility-sensitive`
- Recommended repair slice:
  - introduce an additive validation API, for example
    `ValidateInferenceRequest(ctx, req)` or gateway-owned validation before
    dispatch, backed by the capability model from `P4-CAP-01`
  - define typed validation failures such as `ErrUnsupportedCapability`,
    `ErrUnsupportedModel`, `ErrInvalidRequest`, and model/content requirement
    details that support `errors.Is` and `errors.As`
  - change unsupported streaming from a clean empty channel to a typed setup
    failure or documented terminal stream event after compatibility review
  - thread validation through `DefaultGateway`, interaction gateways, and
    `GatewayInferencer` so direct gateway callers and agent-loop callers observe
    the same unsupported capability result
- Verification notes:
  - later repair lanes should add table-driven gateway tests for tools,
    streaming, sessions, audio input/output, image input, video output,
    reasoning, prompt caching, unsupported model, and invalid provider config,
    asserting typed local failures without live provider calls

#### P4-DI-01: injectable runtime dependencies are uneven across provider constructors and public gateway seams

- Affected package: `github.com/portpowered/go-llm-gateway/pkg/providers/openai`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/anthropic`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/gemini`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/fal`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/grok`,
  `github.com/portpowered/go-llm-gateway/pkg/gateway`
- File path: `go-llm-gateway/pkg/providers/openai/options.go`,
  `go-llm-gateway/pkg/providers/openai/provider.go`,
  `go-llm-gateway/pkg/providers/openai/realtime_dialer.go`,
  `go-llm-gateway/pkg/providers/anthropic/options.go`,
  `go-llm-gateway/pkg/providers/anthropic/provider.go`,
  `go-llm-gateway/pkg/providers/gemini/options.go`,
  `go-llm-gateway/pkg/providers/gemini/provider.go`,
  `go-llm-gateway/pkg/providers/fal/options.go`,
  `go-llm-gateway/pkg/providers/fal/provider.go`,
  `go-llm-gateway/pkg/providers/grok/options.go`,
  `go-llm-gateway/pkg/providers/grok/dialer.go`,
  `go-llm-gateway/pkg/gateway/gateway.go`,
  `go-llm-gateway/pkg/gateway/session_gateway.go`
- Exported declaration: `openai.New`, `openai.WithHTTPClient`,
  `openai.WithBaseURL`, `openai.WithRealtimeBaseURL`,
  `openai.WithWebSocketDialer`, `anthropic.New`,
  `anthropic.WithHTTPClient`, `gemini.New`, `gemini.WithHTTPClient`,
  `fal.New`, `fal.WithHTTPClient`, `grok.New`, `grok.WithWebSocketDialer`,
  `NewGateway`, `NewSessionGateway`
- Observable contract issue:
  - HTTP clients, base URLs, model names, API keys, loggers, and realtime
    WebSocket dialers are injectable for several concrete providers, but not
    through one public gateway-owned dependency contract. A consumer that owns
    replay, test transport, timeout, or endpoint policy must know every concrete
    provider option shape.
  - `OpenAIProvider` falls back to `http.DefaultClient` for stateless HTTP and
    creates a default realtime dialer only when no dialer is set, while
    `GrokSessionProvider` requires a dialer and has no public defaulting at
    `New`. `FalProvider` constructs `&http.Client{}` by default, Anthropic
    captures SDK client construction at `New`, and Gemini creates a GenAI
    client per call. These are all reasonable implementation choices, but the
    exported contracts do not state ownership of timeouts, retry policy, live
    network defaults, SDK client lifecycle, or replay compatibility.
  - There is no exported injection point for retry policy, clock, backoff, or
    per-request timeout policy. Callers can only approximate some of that
    behavior through `context.Context` or custom `http.Client`/dialer options,
    and only for providers that expose those options.
- Mapped checklist rows: `P4-API-06`, `P4-API-04`, `P4-API-07`,
  `P4-API-01`
- Severity: `later polish`
- Compatibility sensitivity: `additive`
- Recommended repair slice:
  - document current dependency ownership on each exported provider constructor
    and option, including whether live network defaults are created, whether
    default clients have timeouts, and who owns retry and close behavior
  - add optional common dependency option structs or small interfaces for
    transports, WebSocket dialers, endpoint/model defaults, timeout policy, and
    retry/backoff policy where provider-neutral ownership is practical
  - keep provider-specific options for provider-specific features, but expose
    gateway composition helpers so record/replay and tests can inject runtime
    dependencies without concrete provider branching in application flow
- Verification notes:
  - later repair lanes should add constructor tests proving no live dependency
    is created when a fake client/dialer is injected, plus replay tests showing
    OpenAI, Anthropic, Gemini, fal, and Grok use the injected transport or
    dialer consistently

#### P4-DI-02: session configuration is bridge-owned but cannot expose provider-specific realtime capability or validation

- Affected package: `github.com/portpowered/go-llm-gateway/pkg/gateway`,
  `github.com/portpowered/go-llm-gateway/pkg/inference`,
  `github.com/portpowered/go-llm-gateway/pkg/models`,
  `github.com/portpowered/go-agent-loop/pkg/messages`
- File path: `go-llm-gateway/pkg/gateway/session_gateway.go`,
  `go-llm-gateway/pkg/gateway/session_types.go`,
  `go-llm-gateway/pkg/inference/session_inferencer.go`,
  `go-llm-gateway/pkg/models/session.go`,
  `go-agent-loop/pkg/messages/session.go`
- Exported declaration: `DefaultSessionGateway.ConnectSession`,
  `SessionGatewayInferencer.ConnectSession`, `models.SessionConfig`,
  `messages.SessionInferencer`, `messages.Session`
- Observable contract issue:
  - `models.SessionConfig` exposes model, voice, instructions, modalities,
    audio formats, sample rates, turn detection, tools, and raw config, but
    `DefaultSessionGateway` forwards it directly to the selected session
    provider. Callers cannot discover which realtime provider supports which
    modalities, audio formats, turn detection fields, tool definitions, sample
    rates, or raw config shape before opening a WebSocket.
  - `SessionGatewayInferencer` owns only model, voice, and instructions options;
    it cannot pass modalities, audio formats, turn detection, tools, sample
    rates, or raw config through the agent-loop `SessionInferencer` seam. This
    makes some provider capabilities available to direct gateway callers but
    not to loop consumers using the exported bridge.
  - Session config validation happens inside provider-specific builders or
    after WebSocket connection setup, so unsupported realtime features can
    create live connections before failing.
- Mapped checklist rows: `P4-API-04`, `P4-API-06`, `P4-API-07`,
  `P4-API-01`, `P4-API-03`
- Severity: `must-fix contract defect`
- Compatibility sensitivity: `compatibility-sensitive`
- Recommended repair slice:
  - add session capability discovery and validation alongside stateless
    provider capability discovery, including modalities, audio formats, turn
    detection, tools, sample rates, raw config, and session lifecycle ownership
  - extend `SessionGatewayInferencer` additively with options or a config
    supplier that can pass the full `models.SessionConfig` expected by the
    selected provider without changing `messages.SessionInferencer`
  - validate unsupported session config before opening a live WebSocket and
    return typed unsupported-capability or invalid-request errors
- Verification notes:
  - later repair lanes should add fake session gateway tests proving
    `SessionGatewayInferencer` forwards full session config and concrete Grok
    and OpenAI realtime tests proving unsupported config fails before dial

#### Provider Capability, Validation, And DI Repair Slice Order

1. Add the provider-neutral capability model first because validation, docs, and
   gateway discovery need one shared vocabulary for supported tools, media,
   reasoning, caching, streaming, sessions, and provider config.
2. Add typed unsupported-capability and invalid-request errors, then implement
   gateway-level request validation for stateless `Infer`/`InferStream`.
3. Repair unsupported streaming behavior, especially fal's clean empty channel,
   after the stream terminal contract and typed validation taxonomy are aligned.
4. Add session capability discovery and pre-dial validation, then extend
   `SessionGatewayInferencer` with additive full-config forwarding.
5. Document and normalize provider dependency ownership after capability and
   validation contracts are in place, keeping provider-specific knobs behind
   small common dependency option shapes where practical.

Reviewer commands for this audit-only story:

```bash
make typecheck
```

No reproducibility tooling was added for this slice; later implementation lanes
must add the tests named in each finding before closing any Phase 4 checklist
row.

### Phase 4 Go API Hygiene And Implementation Order Findings

These findings cover the `P4-API-07` story slice and consolidate the final
implementation order for later Phase 4 repair lanes. They name exported Go API
surfaces where consumers can observe unclear naming, incomplete doc comments,
over-broad public structs, nil/empty slice ambiguity, panic-policy gaps, or
compatibility posture gaps even when the underlying implementation is otherwise
usable.

#### P4-HYGIENE-01: exported message and content contracts lack complete consumer-facing comments

- Affected package: `github.com/portpowered/go-agent-loop/pkg/messages`,
  `github.com/portpowered/go-llm-gateway/pkg/models`
- File path: `go-agent-loop/pkg/messages/agent_messages.go`,
  `go-llm-gateway/pkg/models/message.go`
- Exported declaration: `ContentPart`, `ControlPlanePart`,
  `ControlPlaneMessageType`, `ReasoningPart`, `NewReasoningMessage`,
  `Message`, `Message.TextContent`, `Message.ReasoningContent`,
  `models.ContentPart`, `models.Message`, `models.NewTextMessage`
- Observable contract issue:
  - several exported declarations are undocumented, under-documented, or have
    comments that do not begin with the exported identifier. For example,
    `NewReasoningMessage` is preceded by a stale `NewReason` comment, and
    `ControlPlanePart`, `ControlPlaneMessageType`, and `ReasoningContent` do
    not explain their public role.
  - `models` re-exports loop-owned message types by alias, but package docs do
    not clearly state whether `go-agent-loop/pkg/messages` or
    `go-llm-gateway/pkg/models` is the compatibility anchor for downstream
    consumers.
  - the sealed-interface comment on `ContentPart` names the unexported
    `contentPart()` method, but it does not document whether external packages
    are intentionally prevented from defining custom content parts or how
    callers should represent unsupported media.
- Mapped checklist rows: `P4-API-07`, `P4-API-04`
- Severity: `release/documentation work`
- Compatibility sensitivity: `documentation-only`
- Recommended repair slice:
  - add package-level and exported declaration comments that start with each
    exported identifier and describe caller-visible behavior, especially the
    sealed `ContentPart` contract and model alias ownership
  - document whether downstream code should import `messages` directly,
    `models` aliases, or provider/gateway-level request types for public
    compatibility
  - keep this as documentation-only unless the comment review exposes an
    actual exported naming change that requires compatibility review
- Verification notes:
  - later documentation lanes should run `go test ./...` and optionally
    `go vet ./...` from the affected modules; no source-scanning meta test is
    recommended because comments are documentation contract, not runtime
    behavior

#### P4-HYGIENE-02: broad public request and event structs expose provider-specific fields without nil/zero-value rules

- Affected package: `github.com/portpowered/go-llm-gateway/pkg/providers`,
  `github.com/portpowered/go-llm-gateway/pkg/gateway`,
  `github.com/portpowered/go-agent-loop/pkg/messages`
- File path: `go-llm-gateway/pkg/providers/provider.go`,
  `go-llm-gateway/pkg/gateway/interaction_types.go`,
  `go-agent-loop/pkg/messages/agent_messages.go`,
  `go-agent-loop/pkg/messages/participant_messages.go`
- Exported declaration: `providers.InferenceRequest`,
  `providers.ThinkingConfig`, `providers.CacheControlConfig`,
  `gateway.InteractionRequest`, `gateway.InteractionEvent`,
  `messages.Message`, `messages.StreamMessage`, `messages.ErrorValue`
- Observable contract issue:
  - exported structs expose mutable slices, pointers, raw JSON, and broad
    string fields. Some comments describe provider defaults, but the public API
    does not consistently define nil versus empty slice behavior for
    `Messages`, `Tools`, `StopSequences`, event payloads, tool calls, and
    multimodal content parts.
  - provider-specific knobs such as `Thinking`, `CacheControl`, raw `Config`,
    provider error fields, and interaction event codes live on provider-neutral
    structs. Without capability and validation contracts, callers cannot tell
    whether a zero value means "provider default", "feature disabled",
    "unsupported but ignored", or "invalid request".
  - several event and error fields remain strings rather than typed enums or
    structured result types. This preserves flexibility, but it leaves the
    compatibility posture unclear for event codes, control-plane message types,
    close reasons, provider error details, and future additions.
- Mapped checklist rows: `P4-API-07`, `P4-API-03`, `P4-API-04`,
  `P4-API-02`
- Severity: `must-fix contract defect`
- Compatibility sensitivity: `compatibility-sensitive`
- Recommended repair slice:
  - define nil/empty/zero-value rules for public request and event structs at
    the same time as provider capability and validation work, so unsupported
    features and provider defaults are observable through typed results
  - add typed constants or structured fields for public event/error codes where
    callers already branch on string values; keep existing strings as
    compatibility text during migration
  - document ownership and mutation expectations for slices and raw JSON fields
    passed into gateway/provider calls
- Verification notes:
  - later repair lanes should add runtime tests for nil versus empty messages,
    tools, stop sequences, content parts, event payloads, and raw config,
    asserting typed validation results or documented provider defaults rather
    than source-shape checks

#### P4-HYGIENE-03: public testing and replay fixtures expose mutable shapes without a clear compatibility and panic policy

- Affected package: `github.com/portpowered/go-llm-gateway/pkg/testing`,
  `github.com/portpowered/go-llm-gateway/pkg/gateway`
- File path: `go-llm-gateway/pkg/testing/session_fixture_contract.go`,
  `go-llm-gateway/pkg/testing/session_replay.go`,
  `go-llm-gateway/pkg/testing/session_record.go`,
  `go-llm-gateway/pkg/testing/session_websocket_dialer.go`,
  `go-llm-gateway/pkg/gateway/interaction_fixture.go`
- Exported declaration: `SessionFixture`, `SessionStep`,
  `SessionFixtureContract`, `SessionReplayer`, `SessionRecorder`,
  `ReplayWebSocketDialer`, `InteractionFixture`,
  `InteractionFixtureReplayer`, `LoadInteractionFixture`,
  `DecodeInteractionFixture`, `ValidateInteractionFixture`
- Observable contract issue:
  - fixture structs and replay helpers are exported and suitable for downstream
    test automation, but the public docs do not state which fields are stable
    fixture schema, which are authoring conveniences, and which can evolve
    between harness releases.
  - loader and replayer APIs mostly return errors, but the panic policy for
    malformed fixture values, nil internal state, invalid JSON payloads, and
    replay misuse is not explicitly documented. Consumers building CI replay
    tools need to know whether invalid fixtures are always returned as errors
    or may panic.
  - replay APIs clone fixture values for safety in some paths, but ownership of
    returned slices, maps, raw payloads, and post-construction mutation is not
    consistently part of the exported contract.
- Mapped checklist rows: `P4-API-07`, `P4-API-02`, `P4-API-03`,
  `P4-API-05`
- Severity: `release/documentation work`
- Compatibility sensitivity: `documentation-only`
- Recommended repair slice:
  - document the fixture schema compatibility promise, including which fields
    are stable across releases and how fixture version bumps will be handled
  - state the panic policy for exported fixture/replay APIs: invalid input
    should be returned as a typed error, while panics should be reserved for
    impossible programmer misuse if any such cases remain
  - add or document clone/ownership rules so downstream replay tooling knows
    whether mutating a fixture after constructing a replayer affects playback
- Verification notes:
  - later lanes should add observable fixture tests for malformed input, nil or
    empty fixture values, mutation after replayer construction, close/replay
    misuse, and typed replay errors; no new reproducibility tooling was added
    by this audit

#### P4-HYGIENE-04: exported constructors and options do not publish a module-wide compatibility posture

- Affected package: `github.com/portpowered/go-agent-loop/pkg/agentloop`,
  `github.com/portpowered/go-agent-loop/pkg/participants`,
  `github.com/portpowered/go-llm-gateway/pkg/gateway`,
  `github.com/portpowered/go-llm-gateway/pkg/inference`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/openai`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/anthropic`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/gemini`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/fal`,
  `github.com/portpowered/go-llm-gateway/pkg/providers/grok`
- File path: `go-agent-loop/pkg/agentloop/options.go`,
  `go-agent-loop/pkg/participants/model_runner.go`,
  `go-llm-gateway/pkg/gateway/gateway.go`,
  `go-llm-gateway/pkg/gateway/session_gateway.go`,
  `go-llm-gateway/pkg/inference/main_inferencer.go`,
  `go-llm-gateway/pkg/inference/session_inferencer.go`,
  `go-llm-gateway/pkg/providers/*/options.go`
- Exported declaration: `NewAgentLoop`, `NewModelRunner`,
  `NewSessionModelRunner`, `NewGateway`, `NewSessionGateway`,
  `NewGatewayInferencer`, `NewSessionGatewayInferencer`, provider `New`
  functions, and exported `With*` option functions
- Observable contract issue:
  - constructors and option functions are the main public extension points, but
    the exported docs do not state compatibility expectations for adding new
    options, changing defaults, changing default model names, changing default
    transports, or tightening request validation.
  - default live dependencies and provider defaults affect runtime behavior,
    but package docs do not distinguish stable API shape from configurable
    runtime policy. Consumers cannot tell which defaults are safe to rely on
    versus release notes they must re-check.
  - panic policy is not stated for nil provider/inferencer/session gateway
    inputs, invalid option combinations, or missing runtime dependencies.
    Current code often returns errors from constructors, but this is not a
    documented module-wide rule.
- Mapped checklist rows: `P4-API-07`, `P4-API-06`, `P4-API-01`,
  `P4-API-04`
- Severity: `later polish`
- Compatibility sensitivity: `additive`
- Recommended repair slice:
  - add package docs or constructor comments that define compatibility posture
    for exported options, default model names, default transports, validation
    timing, and nil dependency handling
  - keep new dependency and validation controls additive where possible; mark
    any behavior-tightening defaults as release-note work before merging API
    hardening lanes
  - add tests for nil dependency and invalid option combinations only where
    the exported runtime behavior is promised to callers
- Verification notes:
  - later release/documentation lanes should pair doc updates with focused
    constructor tests for nil provider, nil session provider, missing API key,
    fake transport injection, and invalid option combinations

#### Phase 4 Compatibility And Repair Map

The audit separates the later implementation work into these buckets:

| Bucket | Findings | Compatibility posture | Consuming lane |
| --- | --- | --- | --- |
| Must-fix contract defects | `P4-CTX-01`, `P4-CTX-02`, `P4-CTX-03`, `P4-CTX-04`, `P4-RESULT-01`, `P4-RESULT-02`, `P4-ERR-01`, `P4-ERR-02`, `P4-ERR-03`, `P4-STREAM-01`, `P4-STREAM-02`, `P4-STREAM-03`, `P4-CAP-01`, `P4-VALIDATION-01`, `P4-DI-02`, `P4-HYGIENE-02` | mixed additive and compatibility-sensitive; each implementation lane needs caller-visible tests before closing a checklist row | Phase 4 API hardening lanes |
| Later polish | `P4-DI-01`, `P4-HYGIENE-04` | additive unless defaults or constructor behavior change | Phase 4 dependency ownership or release-hardening lanes |
| Release/documentation work | `P4-HYGIENE-01`, `P4-HYGIENE-03` plus package docs for repaired findings | documentation-only unless docs expose a required naming or signature repair | release notes, package docs, and fixture authoring docs |
| Documentation-only audit output | this Phase 4 report and reviewer commands | documentation-only; audit existence does not close `P4-API-01` through `P4-API-07` | later planners use as source material |

Recommended implementation order:

1. Typed errors: implement `P4-ERR-01`, then mirror the taxonomy into
   `P4-ERR-02` and `P4-ERR-03` so returned errors, stream errors, and replay
   errors support `errors.Is`/`errors.As`.
2. Provider capabilities and request validation: implement `P4-CAP-01`,
   `P4-VALIDATION-01`, and `P4-DI-02`, using `P4-HYGIENE-02` to define
   nil/empty request semantics while validation is added.
3. Stream semantics: implement `P4-STREAM-01`, `P4-STREAM-02`, and
   `P4-STREAM-03` after typed errors exist so terminal stream status can carry
   stable error classes.
4. Context and result behavior: implement `P4-CTX-02`, `P4-CTX-03`,
   `P4-CTX-01`, `P4-CTX-04`, `P4-RESULT-01`, and `P4-RESULT-02`, preserving
   partial results and cancellation status consistently through loop, gateway,
   provider, session, and replay surfaces.
5. Dependency ownership and Go API docs: implement `P4-DI-01`,
   `P4-HYGIENE-01`, `P4-HYGIENE-03`, and `P4-HYGIENE-04` as release and
   documentation follow-up once the behavioral contracts above are stable.

Reviewer commands for this audit-only story:

```bash
make typecheck
make test
```

No reproducibility tooling was added for this slice; later implementation lanes
must add runtime, API, fixture, or emitted-event tests named in each finding
before closing any Phase 4 checklist row.

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

## Phase 3 Shared-Contract Boundary Status

Phase 3 now makes one reviewer-citable boundary choice for `P3-CORE-01` and
the scoped `P3-CORE-02` work:

- `go-agent-loop/pkg/messages` is the authoritative shared contract boundary
  for cross-library message, stream, tool, token-usage, inference, and session
  interfaces
- this slice keeps that boundary in the loop module instead of introducing a
  new shared module because the repository already uses it as the live
  compatibility anchor and does not yet show a lower-risk extraction path
- the required evidence set for reviewers is the package-level documentation in
  `go-agent-loop/pkg/messages`, the dependency guidance in
  `docs/architecture/dependencies.md`, the gateway compatibility-layer
  clarification that follows in later Phase 3 stories, and the automated
  dependency-direction proof in
  `go-agent-loop/test/architecture/dependency_direction_test.go`, the runtime
  adapter proofs in `go-llm-gateway/pkg/inference/main_inferencer_test.go` and
  `go-llm-gateway/pkg/inference/session_inferencer_test.go`, plus the
  supplemental reviewer-facing evidence checks in
  `go-agent-loop/test/architecture/reviewer_evidence_test.go`

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
- Phase 3 status:
  - resolved for the scoped shared-contract decision
  - boundary ownership is now explicit: `go-agent-loop/pkg/messages` is the
    authoritative shared contract package for this slice
  - `go-llm-gateway/pkg/models` is now documented as a compatibility alias
    layer, not an independently owned shared-message vocabulary
  - public adapter packages are documented as bridges into the loop-owned
    boundary, and `go-agent-loop/test/architecture/dependency_direction_test.go`
    together with the runtime adapter proofs in
    `go-llm-gateway/pkg/inference/main_inferencer_test.go` and
    `go-llm-gateway/pkg/inference/session_inferencer_test.go` provide the
    reviewer-citable import-direction and contract-behavior proof that
    preserves this one-way dependency rule
  - `go-agent-loop/test/architecture/reviewer_evidence_test.go` remains as
    supplemental drift protection so the checklist and audit continue citing the
    same evidence set without replacing the runtime adapter proofs
  - reviewers should treat new gateway docs or exports that present
    `pkg/models` as a second shared core surface as a regression against
    `P3-CORE-01` and `P3-CORE-02`

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

### HC-04: gateway provider logging depended on a loop-owned helper package outside the deliberate shared contract boundary

- Affected boundary: `go-llm-gateway/pkg/providers/openai` and `go-llm-gateway/pkg/providers/grok` -> `go-agent-loop/pkg/logging`
- Evidence before the repair:
  - scoped provider and provider-test surfaces imported `go-agent-loop/pkg/logging` directly even though logging is a provider-local concern rather than a loop-owned runtime contract
  - that import path made the gateway-independence story look broader in docs than it was in the actual import graph
- Observable impact before the repair:
  - gateway consumers inherited a loop-owned non-contract runtime helper dependency from provider construction paths
  - reviewers had to reconstruct import details manually to tell whether `go-agent-loop/pkg/messages` was really the only shared runtime dependency
  - the repository lacked one reviewer-facing place that named the replacement seam and its narrower independence claim explicitly
- Why this was hidden coupling instead of an adapter:
  - the intended shared boundary for this slice is `go-agent-loop/pkg/messages`, not loop-owned logging helpers
  - provider logging does not implement a loop-owned interface; it is optional provider-local behavior and belongs behind a gateway-owned seam
- Status after `phase-3-gateway-runtime-decoupling`:
  - resolved for the scoped OpenAI and Grok provider plus provider-test surfaces
  - `go-llm-gateway/pkg/logging` now owns the optional provider logging seam, and `go-agent-loop/pkg/messages` remains the only deliberate shared runtime contract boundary claimed for this slice
  - reviewers should cite `docs/architecture/dependencies.md`, `go-llm-gateway/README.md`, and checklist rows `P3-CORE-04` plus `P3-DOC-01` as the boundary truth for this repair rather than inferring it from code archaeology alone

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
  - narrowed, but not fully resolved, for the scoped session runtime seam
  - `RunSession` now delegates session-mode config loading, dialer selection, and provider-specific runtime construction to `agent-cli/internal/services/session_runtime.go`
  - Grok and OpenAI session providers no longer create hidden live WebSocket dialers inside provider session constructors; missing owned dialers fail explicitly at the provider boundary
  - `agent-cli/internal/services/session_runtime.go` still creates a factory-owned live default through `newDefaultLiveDialer()` on record paths when the caller omits `SessionRunOptions.WebSocketDialer`, so the broader constructor-ownership row remains open until that fallback is removed

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
