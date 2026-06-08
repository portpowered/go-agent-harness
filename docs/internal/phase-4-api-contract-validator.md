# Phase 4 API Contract Validator

## Subject Under Review

This validator reviews the completed Phase 4 public API contract hardening
starter work. Run this pass only after the starter slices under review have
landed and the branch under review is intended to represent the candidate
baseline for the next Phase 4 planning decision.

The validator inspects observable repository state. It does not implement new
Phase 4 API features and does not use broad cleanup or duplicate CI coverage as
a substitute for API contract evidence.

## Scope

This validator records findings for exactly these checklist rows:

- `P4-API-01`: context usage and caller-controlled cancellation/timeout
  contracts
- `P4-API-02`: typed, caller-actionable error contracts
- `P4-API-03`: unambiguous result contracts and failure signals
- `P4-API-04`: provider capability discovery
- `P4-API-05`: stream semantics and error preservation
- `P4-API-06`: local validation of unsupported provider/request features
- `P4-API-07`: dependency injection, provider configuration, and hidden side
  effects
- `P4-GATE-01`: overall public API contract hardening gate readiness

The validator evaluates the completed starter slices for:

1. Exported API contract audit
2. Gateway error taxonomy
3. Provider capability discovery and local request validation

Validation focuses on observable API contract coherence, consumer usability,
and architecture drift across public code, docs, tests, and examples. It does
not reopen unrelated implementation design or use command success as a broad
proxy for contract correctness.

## Evidence Inputs

This convergence pass cites the following authoritative repository surfaces:

- `docs/internal/checklist.md`
- `docs/architecture/contract-gap-audit.md`
- public package documentation and package comments for changed surfaces
- tests and examples that prove public API behavior without live credentials or
  external network access
- reviewer-runnable local commands that make each reported claim reproducible

## Required Finding Shape

Use this shape for every row-level finding:

### `[Checklist Row ID]` - `[Row Area]`

- `outcome`: `pass` | `fail` | `uncertain`
- `evidence`:
- `affected files / declarations`:
- `closure decision`: `may close` | `must remain open`
- `exact repair work`:
- `reviewer commands`:

Every non-pass outcome must include exact repair work. A `pass` outcome must
include enough evidence for a reviewer to rerun or inspect the claim without
recovering planner intent from previous work items.

## Closure Rules

- A row may close only when the report ties the completed starter slices to
  observable repository evidence for that row and the evidence includes public
  consumer guidance where the changed surface is public.
- A row must remain open when evidence is missing, only inferred from intent,
  unavailable without credentials or network access, string-only where typed or
  structured behavior is required, or too broad for an implementer to repair in
  one scoped follow-up batch.
- `P4-GATE-01` may close only when the row-level report gives planners one of
  these exact next actions: `repair`, `cleanup/reconciliation`, or `next Phase
  4 feature batch`.

## Audit-To-Checklist Coverage

This section validates the exported API contract audit coverage only. Audit
evidence can justify planning and repair scope, but it cannot close an
implementation checklist row by itself unless the row requires only an audit
finding.

The current audit at `docs/architecture/contract-gap-audit.md` now distinguishes
several Phase 4 repair classes:

- context and result defects: `P4-CTX-*` and `P4-RESULT-*`
- typed-error defects: `P4-ERR-*`
- stream semantics defects: `P4-STREAM-*`
- provider capability and validation defects: `P4-CAP-01`,
  `P4-VALIDATION-01`, and session capability/validation evidence in `P4-DI-02`
- dependency ownership defects and polish: `P4-DI-*`
- API hygiene, documentation, and compatibility work: `P4-HYGIENE-*`

The audit is now explicit enough to cover provider capability discovery and
local unsupported-feature validation as audit findings. Remaining failures for
`P4-API-04` and `P4-API-06` are implementation, public documentation, examples,
and credential-free test gaps, not missing audit-coverage gaps. Audit evidence
still cannot close a Phase 4 implementation checklist row by itself.

### `P4-API-01` - Context and cancellation contracts

- `outcome`: `uncertain`
- `evidence`:
  - `P4-CTX-01` and `P4-CTX-03` record context contract gaps in
    `go-agent-loop/pkg/messages.SessionInferencer`,
    `go-llm-gateway/pkg/inference.SessionGatewayInferencer`, and
    `agent-cli/internal/services/session.go`: cancellation is explicit through
    `ConnectSession(ctx context.Context)`, but per-session request shape is
    split across constructor options and CLI/provider wiring.
  - `P4-CTX-02` records that buffer waits collapse cancellation, empty buffer,
    and backpressure into `false`, which affects session send/receive behavior.
  - The audit covers session context and relay cancellation, but does not map
    context and timeout behavior across every checklist primary surface:
    `go-agent-loop/pkg/agentloop`, `go-agent-loop/pkg/engine`,
    `go-llm-gateway/pkg/gateway`, `go-llm-gateway/pkg/inference`, provider
    packages, public docs, tests, and examples.
- `affected files / declarations`:
  - `go-agent-loop/pkg/messages.SessionInferencer`
  - `go-llm-gateway/pkg/inference.SessionGatewayInferencer`
  - `agent-cli/internal/services/session.go`
  - `go-llm-gateway/pkg/testing.SessionRecorder`
  - `go-llm-gateway/pkg/testing.SessionReplayer`
- `closure decision`: `must remain open`
- `exact repair work`:
  - Add explicit `P4-API-01` mapping in the audit for all blocking/provider
    public entrypoints, including affected declarations, timeout/cancellation
    contract status, and docs/tests/examples evidence.
  - Separate already-repaired relay cancellation evidence from remaining
    session request-shape work so planners can queue one implementation slice
    instead of broad context cleanup.
- `reviewer commands`:
  - `sed -n '149,187p' docs/architecture/contract-gap-audit.md`
  - `sed -n '150p' docs/internal/checklist.md`

### `P4-API-02` - Typed caller-actionable errors

- `outcome`: `uncertain`
- `evidence`:
  - `P4-ERR-01` records that provider status and transport errors are not
    represented by a shared typed error taxonomy.
  - `P4-ERR-02` records that stream error events carry free-form messages
    instead of caller-actionable error classes.
  - `P4-ERR-03` records that replay mismatch and fixture validation errors are
    public but not integrated into the shared error model.
  - The audit classifies typed-error work as must-fix, mixed additive, and
    compatibility-sensitive in the Phase 4 closure table, which distinguishes
    contract work from migration risk.
  - The audit does not enumerate every provider/gateway/replay validation
    declaration affected by typed error work and does not state which
    audit-backed rows may close after taxonomy implementation evidence exists.
- `affected files / declarations`:
  - `go-agent-loop/pkg/messages.ErrorValue`
  - `go-agent-loop/pkg/participants.ModelRunner`
  - `go-agent-loop/pkg/participants.ToolRunner`
  - provider stream adapters
  - `agent-cli/internal/services/session.go`
- `closure decision`: `must remain open`
- `exact repair work`:
  - Add explicit `P4-API-02` audit row mapping that names the public gateway,
    provider, replay, validation, and cancellation declarations affected by
    typed error taxonomy work.
  - Split repair slices into additive typed taxonomy introduction,
    representative preservation tests, and public caller guidance so the audit
    is not only a broad "typed errors" recommendation.
- `reviewer commands`:
  - `sed -n '188,224p' docs/architecture/contract-gap-audit.md`
  - `sed -n '318,327p' docs/architecture/contract-gap-audit.md`
  - `sed -n '151p' docs/internal/checklist.md`

### `P4-API-03` - Result contracts and failure signals

- `outcome`: `uncertain`
- `evidence`:
  - `P4-RESULT-01` records zero-value text and fixture helper contracts that
    make invalid, absent, and intentionally empty results hard to distinguish.
  - `P4-RESULT-02` records inconsistent interaction and tool-result validation
    errors across sync, event, and fixture APIs.
  - `P4-STREAM-01` records that streaming APIs do not expose one terminal error
    and final-event contract, which keeps result completion ambiguous.
  - The audit covers session and streaming result ambiguity, but it does not
    map public result contracts in `go-agent-loop/pkg/agentloop`,
    `go-agent-loop/pkg/subsystems`, `go-llm-gateway/pkg/gateway`, or
    `go-llm-gateway/pkg/models` row-by-row.
- `affected files / declarations`:
  - `go-agent-loop/pkg/participants.ModelRunner`
  - `agent-cli/internal/services/session.go`
  - `go-agent-loop/pkg/messages.Message`
  - `go-agent-loop/pkg/messages.StreamMessage`
  - `go-llm-gateway/pkg/models`
- `closure decision`: `must remain open`
- `exact repair work`:
  - Add explicit `P4-API-03` audit mapping for public result and stream-event
    declarations, including success, partial success, terminal failure, replay
    divergence, cancellation, provider rejection, and synthesized completion.
  - Identify which lifecycle repairs are documentation-only versus
    implementation changes that require fixture and CLI replay updates.
- `reviewer commands`:
  - `sed -n '225,259p' docs/architecture/contract-gap-audit.md`
  - `sed -n '292,317p' docs/architecture/contract-gap-audit.md`
  - `sed -n '152p' docs/internal/checklist.md`

### `P4-API-04` - Provider capability discovery

- `outcome`: `fail`
- `evidence`:
  - `P4-CAP-01` maps to `P4-API-04`, `P4-API-06`, `P4-API-07`,
    `P4-API-02`, and `P4-API-03` and names the public gateway, provider, and
    model declarations where capability support is not discoverable.
  - `P4-CAP-01` records affected packages, files, exported declarations,
    observable issues, severity, compatibility sensitivity, repair slices, and
    verification notes for tools, streaming, sessions, audio input/output,
    image input, video output, reasoning, prompt caching, embeddings, provider
    config, and provider-specific limits.
  - `P4-DI-02` adds session capability evidence for `models.SessionConfig`,
    `DefaultSessionGateway.ConnectSession`, and
    `SessionGatewayInferencer.ConnectSession`.
  - Runtime implementation evidence still fails this row: `Provider`,
    `SessionProvider`, `Gateway`, `DefaultGateway`, and
    `DefaultSessionGateway` do not expose a public capability API that
    consumers can query before sending a request.
- `affected files / declarations`:
  - `go-llm-gateway/pkg/providers.Provider`
  - `go-llm-gateway/pkg/providers.SessionProvider`
  - `go-llm-gateway/pkg/providers.InferenceRequest`
  - `go-llm-gateway/pkg/gateway.Gateway`
  - `go-llm-gateway/pkg/gateway.DefaultGateway`
  - `go-llm-gateway/pkg/gateway.DefaultSessionGateway`
  - `go-llm-gateway/pkg/models.Message`
  - `go-llm-gateway/pkg/models.ContentPart`
  - `go-llm-gateway/pkg/models.ToolDefinition`
  - `go-llm-gateway/pkg/models.SessionConfig`
- `closure decision`: `must remain open`
- `exact repair work`:
  - Implement the audit repair slice from `P4-CAP-01`: add a provider-neutral
    exported capability model and a gateway-level discovery method so
    consumers do not need concrete provider imports or provider type switches.
  - Include supported, unsupported, and unknown semantics for tools, streaming,
    sessions, audio, image input, video output, reasoning, prompt caching,
    provider config, and provider-specific limits.
  - Add public docs, examples, and credential-free tests that prove capability
    discovery through gateway/provider APIs.
- `reviewer commands`:
  - `rg -n "capabil|Capability|Capabilities" docs/architecture/contract-gap-audit.md go-llm-gateway/pkg`
  - `sed -n '696,746p' docs/architecture/contract-gap-audit.md`
  - `sed -n '153p' docs/internal/checklist.md`

### `P4-API-05` - Stream semantics and error preservation

- `outcome`: `uncertain`
- `evidence`:
  - `P4-ERR-02` covers stream error classification collapse into free-form
    messages.
  - `P4-STREAM-01`, `P4-STREAM-02`, and `P4-STREAM-03` cover terminal stream
    status, provider adapter ordering, session close, and cancellation
    semantics.
  - `P4-ERR-03` covers replay mismatch and fixture validation errors that are
    public but not integrated into the shared error model.
  - The audit covers several stream semantics issues, but it does not map
    replay mismatch preservation, provider-close classification, and
    credential-free tests/examples to the `P4-API-05` row explicitly.
- `affected files / declarations`:
  - `go-agent-loop/pkg/messages.ErrorValue`
  - `go-agent-loop/pkg/participants.ModelRunner`
  - `go-llm-gateway/pkg/testing.SessionRecorder`
  - `go-llm-gateway/pkg/testing.SessionReplayer`
  - provider stream adapters
- `closure decision`: `must remain open`
- `exact repair work`:
  - Add explicit `P4-API-05` audit mapping that joins stream lifecycle,
    cancellation, replay mismatch, provider-close, and error-classification
    evidence under one row.
  - Name the changed public stream/session declarations and distinguish
    already-repaired relay cancellation from remaining stream taxonomy and
    lifecycle repairs.
- `reviewer commands`:
  - `sed -n '188,259p' docs/architecture/contract-gap-audit.md`
  - `sed -n '169,187p' docs/architecture/contract-gap-audit.md`
  - `sed -n '154p' docs/internal/checklist.md`

### `P4-API-06` - Local unsupported-feature validation

- `outcome`: `fail`
- `evidence`:
  - `P4-VALIDATION-01` maps unsupported request feature behavior to
    `P4-API-06` and records that `DefaultGateway`, `Provider.Infer`,
    `Provider.InferStream`, `GatewayInferencer.Infer`, and
    `GatewayInferencer.InferStream` do not share a local validation step before
    provider execution.
  - `P4-VALIDATION-01` identifies inconsistent provider behavior: unsupported
    fields may be ignored, translated to provider-specific params, returned as
    formatted string errors, or represented by fal's immediately closed stream
    channel.
  - `P4-CAP-01` supplies the required capability model prerequisite, and
    `P4-DI-02` records session pre-dial validation gaps for realtime
    modalities, audio formats, turn detection, tools, sample rates, and raw
    config.
  - Runtime implementation evidence still fails this row: there is no shared
    public local validation API or typed validation taxonomy that reports the
    provider, requested feature or mode, and capability state before provider
    dispatch.
- `affected files / declarations`:
  - `go-llm-gateway/pkg/gateway.DefaultGateway.Infer`
  - `go-llm-gateway/pkg/gateway.DefaultGateway.InferStream`
  - `go-llm-gateway/pkg/gateway.InferenceRequest`
  - `go-llm-gateway/pkg/providers.Provider.Infer`
  - `go-llm-gateway/pkg/providers.Provider.InferStream`
  - `go-llm-gateway/pkg/providers.InferenceRequest`
  - `go-llm-gateway/pkg/inference.GatewayInferencer.Infer`
  - `go-llm-gateway/pkg/inference.GatewayInferencer.InferStream`
  - `go-llm-gateway/pkg/models.SessionConfig`
- `closure decision`: `must remain open`
- `exact repair work`:
  - Implement the audit repair slice from `P4-VALIDATION-01`: add capability-
    backed validation before stateless and session provider dispatch.
  - Define typed validation failures such as unsupported capability,
    unsupported model, and invalid request details that support `errors.Is` and
    `errors.As`.
  - Add public docs, examples, and credential-free tests proving unsupported
    features fail locally without HTTP, SDK, websocket, or live-provider side
    effects.
- `reviewer commands`:
  - `rg -n "unsupported|validate|validation|capabil|feature" docs/architecture/contract-gap-audit.md go-llm-gateway/pkg`
  - `sed -n '747,801p' docs/architecture/contract-gap-audit.md`
  - `sed -n '155p' docs/internal/checklist.md`

### `P4-API-07` - Dependency injection and hidden side effects

- `outcome`: `uncertain`
- `evidence`:
  - `P4-DI-01` records uneven injectable runtime dependencies across provider
    constructors and public gateway seams.
  - `P4-DI-02` records that session configuration is bridge-owned but cannot
    expose provider-specific realtime capability or validation.
  - `P4-HYGIENE-04` records that exported constructors and options do not
    publish a module-wide compatibility posture.
  - The audit distinguishes completed prerequisite repairs from remaining
    dependency defects, but it does not provide a Phase 4 row-level closure
    decision for every public constructor/composition seam in the checklist.
- `affected files / declarations`:
  - `go-agent-loop/pkg/agentloop.New`
  - `agent-cli/internal/agent.buildProviderHTTPRuntime`
  - `agent-cli/internal/services/session_runtime.go`
  - `agent-cli/internal/agent.Executor.loadSystemPrompt`
  - `go-llm-gateway/pkg/providers/openai`
  - `go-llm-gateway/pkg/providers/grok`
- `closure decision`: `must remain open`
- `exact repair work`:
  - Add explicit `P4-API-07` mapping that separates closed prerequisite
    dependency-injection repairs from still-open hidden side effects.
  - Name public and internal composition declarations whose ownership decisions
    are user-facing through constructors, CLI behavior, docs, tests, or
    examples, and state which are compatibility-sensitive.
- `reviewer commands`:
  - `sed -n '74,148p' docs/architecture/contract-gap-audit.md`
  - `sed -n '52,73p' docs/architecture/contract-gap-audit.md`
  - `sed -n '156p' docs/internal/checklist.md`

### `P4-GATE-01` - Public API hardening gate readiness

- `outcome`: `fail`
- `evidence`:
  - Audit-backed coverage now exists for provider capability discovery and
    local unsupported-feature validation through `P4-CAP-01`,
    `P4-VALIDATION-01`, and `P4-DI-02`. `P4-API-04` and `P4-API-06` still
    fail because implementation, public docs, examples, and credential-free
    behavioral tests are missing, not because the audit lacks those findings.
  - `P4-API-01`, `P4-API-02`, `P4-API-03`, `P4-API-05`, and `P4-API-07`
    have useful audit findings, but they still need explicit Phase 4 row
    mappings, affected public declarations, docs/tests/examples evidence, and
    scoped repair batches.
  - No Phase 4 checklist row may close from audit evidence alone in the current
    repository state.
- `affected files / declarations`:
  - `docs/architecture/contract-gap-audit.md`
  - `docs/internal/checklist.md`
  - `docs/internal/phase-4-api-contract-validator.md`
- `closure decision`: `must remain open`
- `exact repair work`:
  - Preserve the exported API contract audit mappings for every Phase 4 row and
    keep them reconciled with this validator as the implementation surface
    changes.
  - Do not queue the next Phase 4 feature batch until provider capability
    discovery, local unsupported-feature validation, typed errors, stream
    semantics, dependency injection, public docs, examples, and tests have
    implementation evidence that satisfies the row-level closure rules.
- `reviewer commands`:
  - `sed -n '141,157p' docs/internal/checklist.md`
  - `sed -n '688,930p' docs/architecture/contract-gap-audit.md`

## Audit Coverage Closure Summary

No checklist row may close from audit evidence alone in this story. The audit
does provide useful must-fix and compatibility-sensitive evidence for every
reviewed Phase 4 row, including provider capability discovery in `P4-CAP-01`,
local unsupported-feature validation in `P4-VALIDATION-01`, and session
capability/validation gaps in `P4-DI-02`. Each row remains open because audit
coverage is planning evidence, while closure requires implementation evidence,
public consumer guidance, examples where useful, and credential-free tests that
prove the public contract is usable.

## Gateway Error Taxonomy Evidence

This section validates the implemented gateway error taxonomy and preservation
evidence. It focuses on public caller behavior rather than only audit intent:
typed errors must be usable through `errors.Is` or `errors.As`, and structured
event errors must be documented fields that callers can branch on without
parsing message text.

The current repository has partial structured event evidence for the
provider-neutral interaction surface, but it does not yet expose a public typed
gateway error taxonomy across the main gateway, provider, replay, validation,
and session surfaces.

### `P4-API-02` - Typed caller-actionable errors

- `outcome`: `fail`
- `evidence`:
  - `go-llm-gateway/pkg/gateway.DefaultGateway.Infer` and
    `DefaultGateway.InferStream` forward provider errors directly. There are no
    public gateway sentinel errors, typed error structs, or helper functions
    that let callers classify provider rejection, provider timeout, replay
    divergence, validation failure, or cancellation with `errors.Is` or
    `errors.As`.
  - `go-llm-gateway/pkg/gateway.Interact` emits structured
    `InteractionError` values with `Code`, `Message`, `Retryable`, and
    `Details` fields. Tests prove the provider error path emits
    `provider_error`, deadline errors emit `provider_timeout` with
    `Retryable: true`, and pre-provider cancellation emits
    `InteractionCancellation` with reason `caller_cancelled`.
  - The interaction event structure is useful for callers consuming
    `Interact`, but the taxonomy is string-coded event data rather than typed
    Go errors. It also does not cover the main `Infer`/`InferStream` return
    paths or replay/validation errors.
  - Public README guidance describes package surfaces and provider capability
    limits, but it does not document gateway error classes, `errors.Is` /
    `errors.As` usage, stream error preservation, replay divergence
    classification, or known classification limits.
- `affected files / declarations`:
  - `go-llm-gateway/pkg/gateway.DefaultGateway.Infer`
  - `go-llm-gateway/pkg/gateway.DefaultGateway.InferStream`
  - `go-llm-gateway/pkg/gateway.DefaultGateway.Interact`
  - `go-llm-gateway/pkg/gateway.InteractionError`
  - `go-llm-gateway/pkg/gateway.InteractionCancellation`
  - `go-llm-gateway/pkg/testing.SessionReplayer.Err`
  - `go-llm-gateway/pkg/testing.ReplayWebSocketDialer.Err`
  - `go-llm-gateway/README.md`
- `closure decision`: `must remain open`
- `exact repair work`:
  - Add a public gateway error taxonomy using exported sentinels, typed error
    structs, or documented helpers that support `errors.Is` or `errors.As` for
    provider rejection, timeout, cancellation, replay divergence, validation
    failure, and unsupported feature classes.
  - Wrap or translate `Infer`, `InferStream`, interaction event generation,
    session replay, and validation failures so representative paths preserve
    the intended class without forcing callers to parse `err.Error()` or
    string event codes.
  - Document caller branching guidance and known classification limits in
    public gateway docs or package comments, including how structured
    `InteractionError.Code` relates to the typed error taxonomy.
  - Add credential-free tests proving `errors.Is` or `errors.As` behavior for
    stateless non-streaming errors, stateless stream error events, replay
    mismatch errors, and cancellation errors.
- `reviewer commands`:
  - `go test ./go-llm-gateway/pkg/gateway -run 'TestInteract_(NormalizesProviderError|EmitsCancellationWhenContextCancelledBeforeProviderReturns|PreservesPartialOutputBeforeCancellation)'`
  - `go test ./go-llm-gateway/pkg/testing -run 'TestSessionReplayer_(FailsOnUnexpectedOutboundEvent|FailsWhenExpectedOutboundIsOmitted)'`
  - `rg -n "errors\\.Is|errors\\.As|InteractionError|InteractionCancellation|provider_error|provider_timeout|caller_cancelled" go-llm-gateway/pkg/gateway go-llm-gateway/pkg/testing go-llm-gateway/README.md`

### `P4-API-05` - Stream semantics and error preservation

- `outcome`: `uncertain`
- `evidence`:
  - The interaction surface preserves cancellation as a separate terminal
    event, and tests prove partial text output can be emitted before a later
    cancellation event. That is useful stream-result evidence for
    provider-neutral interaction consumers.
  - `DefaultGateway.InferStream` still returns the provider stream directly.
    Stream error preservation therefore depends on provider adapters and
    `messages.StreamMessage` values rather than a gateway-level typed taxonomy.
  - The loop message contract still represents stream errors through
    `messages.ErrorValue` with string `Message`, optional `Code`, and
    `Metadata` fields. The validator did not find public package guidance that
    maps those stream error fields to gateway taxonomy classes.
  - Replay mismatch errors from `SessionReplayer` and `ReplayWebSocketDialer`
    are explicit and tested, but they are plain formatted errors rather than a
    typed replay divergence class.
- `affected files / declarations`:
  - `go-llm-gateway/pkg/gateway.DefaultGateway.InferStream`
  - `go-llm-gateway/pkg/gateway.DefaultGateway.Interact`
  - `go-llm-gateway/pkg/gateway.InteractionEvent`
  - `go-agent-loop/pkg/messages.ErrorValue`
  - `go-llm-gateway/pkg/testing.SessionReplayer.Err`
  - `go-llm-gateway/pkg/testing.ReplayWebSocketDialer.Err`
- `closure decision`: `must remain open`
- `exact repair work`:
  - Define how stream errors map to the public gateway taxonomy across direct
    `InferStream`, interaction events, provider stream adapters, and session
    replay helpers.
  - Preserve replay divergence and cancellation as typed or structured classes
    through stream consumers instead of exposing only formatted text.
  - Add public docs and credential-free tests that distinguish partial success,
    terminal failure, cancellation, replay mismatch, and provider rejection.
- `reviewer commands`:
  - `go test ./go-llm-gateway/pkg/gateway -run 'TestInteract_PreservesPartialOutputBeforeCancellation'`
  - `go test ./go-llm-gateway/pkg/testing -run 'TestSessionReplayer_(FailsOnUnexpectedOutboundEvent|StopsDeliveryWhenOwnedContextCanceled)'`
  - `rg -n "StreamTypeError|NewErrorValue|ErrorValue|replay divergence|caller_cancelled" go-agent-loop/pkg/messages go-llm-gateway/pkg go-llm-gateway/README.md`

## Gateway Error Taxonomy Closure Summary

`P4-API-02` fails because the implemented evidence is structured event data for
one interaction surface, not a public typed error taxonomy across gateway,
provider, replay, validation, and cancellation paths. `P4-API-05` remains
uncertain because interaction cancellation and partial-output behavior are
tested, but direct streaming and replay mismatch paths are not yet tied to
typed or documented taxonomy classes. These findings reinforce the earlier
audit conclusion that both rows must remain open until implementation evidence,
public guidance, and credential-free taxonomy tests exist.

## Provider Capability and Validation Evidence

This section validates whether the completed provider capability discovery and
local request validation slice is present in public, consumer-usable
`go-llm-gateway` APIs. The review distinguishes documentation tables from a
runtime capability contract: README guidance can help consumers, but it does
not let applications gate requests programmatically or inspect local validation
failures without importing concrete provider internals.

The current repository exposes provider names, stateless and session provider
interfaces, request fields, provider-specific option structs, and a README
provider surface map. It does not yet expose a public capability data model,
supported/unsupported/unknown semantics, or gateway-level validation that
rejects unsupported features before provider execution.

### `P4-API-04` - Provider capability discovery

- `outcome`: `fail`
- `evidence`:
  - `providers.Provider` exposes `Name`, `Infer`, and `InferStream`; it has no
    `Capabilities` method or exported capability value that applications can
    query through `go-llm-gateway/pkg/providers` or `go-llm-gateway/pkg/gateway`.
  - `providers.SessionProvider` exposes `Name` and `ConnectSession`; it also
    has no session capability discovery API. Session availability can only be
    inferred from which concrete provider interface a caller imported or wired.
  - `gateway.DefaultGateway` and `DefaultSessionGateway` only forward requests
    to configured providers. They do not expose provider capabilities or
    normalize stateless/session support into one public inspection surface.
  - `go-llm-gateway/README.md` includes a provider surface map for stateless
    `Infer`, stateless `InferStream`, and session `ConnectSession`, plus notes
    for Anthropic thinking/cache controls, OpenAI-compatible base URLs and
    sessions, Grok sessions, and fal media flows. This is static documentation,
    not a public runtime API.
  - Capability fields required by the story are missing as an inspectable
    contract: tools, streaming, sessions, audio, image input, video output,
    reasoning, prompt caching, and provider-specific config do not have shared
    supported, unsupported, or unknown states.
- `affected files / declarations`:
  - `go-llm-gateway/pkg/providers.Provider`
  - `go-llm-gateway/pkg/providers.SessionProvider`
  - `go-llm-gateway/pkg/providers.InferenceRequest`
  - `go-llm-gateway/pkg/gateway.Gateway`
  - `go-llm-gateway/pkg/gateway.DefaultGateway`
  - `go-llm-gateway/pkg/gateway.DefaultSessionGateway`
  - `go-llm-gateway/pkg/models.SessionConfig`
  - provider constructors in `go-llm-gateway/pkg/providers/anthropic`,
    `go-llm-gateway/pkg/providers/openai`, `go-llm-gateway/pkg/providers/gemini`,
    `go-llm-gateway/pkg/providers/grok`, and
    `go-llm-gateway/pkg/providers/fal`
  - `go-llm-gateway/README.md`
- `closure decision`: `must remain open`
- `exact repair work`:
  - Add a public provider capability contract in `go-llm-gateway/pkg/providers`
    or `go-llm-gateway/pkg/gateway` that callers can query without importing
    concrete provider internals.
  - Model capability states explicitly as supported, unsupported, or unknown
    for tools, streaming, sessions, audio, image input, video output,
    reasoning, prompt caching, and provider-specific config.
  - Wire concrete providers to report capabilities through that public
    contract, including stateless-only, session-only, media-specific, and
    provider-specific configuration support.
  - Document the runtime capability API in public package comments or README
    examples, including how it relates to provider package selection.
  - Add credential-free tests that assert capability discovery through the
    public API for representative providers without live credentials or network
    access.
- `reviewer commands`:
  - `rg -n "type Provider interface|type SessionProvider interface|type Gateway interface|type InferenceRequest struct|type SessionConfig struct" go-llm-gateway/pkg/providers go-llm-gateway/pkg/gateway go-llm-gateway/pkg/models`
  - `rg -n "Capability|Capabilities|capability|capabilities" go-llm-gateway/pkg/providers go-llm-gateway/pkg/gateway go-llm-gateway/README.md`
  - `sed -n '172,198p' go-llm-gateway/README.md`

### `P4-API-06` - Local unsupported-feature validation

- `outcome`: `fail`
- `evidence`:
  - `gateway.DefaultGateway.Infer` and `DefaultGateway.InferStream` copy every
    request field to `providers.InferenceRequest` and immediately call the
    configured provider. There is no gateway-level validation step that checks
    requested tools, streaming, sessions, audio, image input, video output,
    reasoning, prompt caching, or raw provider-specific config against provider
    capabilities before provider execution.
  - `DefaultSessionGateway.ConnectSession` forwards `models.SessionConfig`
    directly to the configured session provider. It does not locally validate
    requested modalities, audio formats, tools, turn detection, model, or raw
    config against an inspectable capability contract.
  - Provider-specific behavior is inconsistent rather than a shared validation
    contract. Anthropic maps thinking and cache-control options; OpenAI ignores
    `Thinking`; fal returns string errors for missing or unsupported model
    values and returns a closed stream for `InferStream` even though README
    marks fal streaming as unsupported; OpenAI and Grok session providers fail
    locally for missing websocket dialers, but those errors do not identify a
    requested unsupported feature, capability state, or shared validation class.
  - Tests prove request passthrough, provider-specific option mapping, fal
    model errors, and session dialer preconditions, but they do not prove a
    public local unsupported-feature validation contract before provider
    execution.
  - Public docs describe provider differences, but they do not document a
    structured validation error carrying provider, requested feature or mode,
    and capability state.
- `affected files / declarations`:
  - `go-llm-gateway/pkg/gateway.DefaultGateway.Infer`
  - `go-llm-gateway/pkg/gateway.DefaultGateway.InferStream`
  - `go-llm-gateway/pkg/gateway.DefaultSessionGateway.ConnectSession`
  - `go-llm-gateway/pkg/gateway.InferenceRequest`
  - `go-llm-gateway/pkg/providers.InferenceRequest`
  - `go-llm-gateway/pkg/models.SessionConfig`
  - `go-llm-gateway/pkg/providers/anthropic.applyInferenceRequestOptions`
  - `go-llm-gateway/pkg/providers/openai.applyInferenceRequestOptions`
  - `go-llm-gateway/pkg/providers/fal.FalProvider.Infer`
  - `go-llm-gateway/pkg/providers/fal.FalProvider.InferStream`
  - `go-llm-gateway/pkg/providers/openai.OpenAIProvider.ConnectSession`
  - `go-llm-gateway/pkg/providers/grok.GrokSessionProvider.ConnectSession`
- `closure decision`: `must remain open`
- `exact repair work`:
  - Add local request validation before provider execution in the gateway or a
    shared provider validation layer, using the public capability contract from
    `P4-API-04`.
  - Return structured or typed validation errors that identify provider name,
    requested feature or mode, capability state, and whether the failure was
    local validation rather than a live provider rejection.
  - Cover stateless and session requests, including tools, streaming, sessions,
    audio, image input, video output, reasoning, prompt caching, and
    provider-specific config.
  - Decide and document the fal streaming behavior: either reject
    `InferStream` locally as unsupported with an inspectable validation error,
    or document the closed-channel behavior as an intentional no-op contract
    and expose it through capabilities.
  - Add credential-free tests with fake providers or provider-local test
    doubles proving unsupported features are rejected before HTTP, SDK,
    websocket, or provider execution side effects.
- `reviewer commands`:
  - `sed -n '28,66p' go-llm-gateway/pkg/gateway/gateway.go`
  - `sed -n '51,53p' go-llm-gateway/pkg/gateway/session_gateway.go`
  - `go test ./go-llm-gateway/pkg/inference -run 'TestInferStream_PassthroughAllFields|TestSessionGatewayInferencer_ConnectSession'`
  - `go test ./go-llm-gateway/pkg/providers/fal -run 'TestFalProvider_(Infer_InvalidRequests|InferStream_ReturnsClosedChannel)'`
  - `go test ./go-llm-gateway/pkg/providers/openai -run 'TestConnectSession_(MissingAPIKeyFailsBeforeDial|MissingDialerFailsBeforeDial)|TestApplyInferenceRequestOptions_ThinkingIgnored'`
  - `go test ./go-llm-gateway/pkg/providers/grok -run 'TestConnectSession_MissingDialerFailsBeforeDial'`

## Provider Capability and Validation Closure Summary

`P4-API-04` and `P4-API-06` both fail. The repository has useful static README
guidance and provider-specific tests, but no public runtime capability API, no
shared supported/unsupported/unknown capability semantics, and no structured
local unsupported-feature validation contract before provider execution. The
next implementation batch for these rows should introduce the public
capability contract first, then use it as the source of truth for local
stateless and session validation.

## Reviewer-Runnable Command Evidence

Run all commands from the repository root. These commands do not require live
provider credentials, external network access, or hidden local setup. They
exercise the current public API contract evidence directly enough for a
reviewer to reproduce the pass, fail, or uncertain outcomes in this report.

| Command | Claim proved | Expected pass condition | Relevant rows |
| --- | --- | --- | --- |
| `sed -n '141,157p' docs/internal/checklist.md` | The validator is checking the Phase 4 rows `P4-API-01` through `P4-API-07` and `P4-GATE-01`. | Output names all reviewed rows and their row text. | all rows |
| `sed -n '149,327p' docs/architecture/contract-gap-audit.md` | The exported API contract audit contains context, typed-error, lifecycle, stream, and compatibility findings for early Phase 4 rows. | Output shows audit findings such as `P4-CTX-*`, `P4-ERR-*`, and result/stream contract findings, supporting the audit-to-checklist evidence for these rows. | `P4-API-01`, `P4-API-02`, `P4-API-03`, `P4-API-05`, `P4-GATE-01` |
| `sed -n '688,930p' docs/architecture/contract-gap-audit.md` | The exported API contract audit now covers provider capability discovery, unsupported-feature validation, and related session/dependency gaps. | Output shows `P4-CAP-01`, `P4-VALIDATION-01`, and `P4-DI-02`, with affected packages, exported declarations, repair slices, and verification notes. | `P4-API-04`, `P4-API-06`, `P4-API-07`, `P4-GATE-01` |
| `rg -n "capabil|Capability|Capabilities" docs/architecture/contract-gap-audit.md go-llm-gateway/pkg` | Capability discovery is covered by the audit, but not yet exposed as a public runtime provider/gateway capability contract. | Output shows `P4-CAP-01` audit evidence and current package references; it must not show a public capability model on `providers.Provider`, `providers.SessionProvider`, or gateway interfaces. | `P4-API-04`, `P4-GATE-01` |
| `rg -n "unsupported|validate|validation|capabil|feature" docs/architecture/contract-gap-audit.md go-llm-gateway/pkg` | Unsupported-feature validation is covered by the audit, but not yet implemented as a shared gateway/provider contract before provider execution. | Output shows `P4-VALIDATION-01` audit evidence and scattered provider-specific validation, but no shared public capability-backed validation layer. | `P4-API-06`, `P4-GATE-01` |
| `rg -n "type Provider interface|type SessionProvider interface|type Gateway interface|type InferenceRequest struct|type SessionConfig struct" go-llm-gateway/pkg/providers go-llm-gateway/pkg/gateway go-llm-gateway/pkg/models` | The public provider, gateway, request, and session declarations are inspectable without importing concrete provider internals. | Output identifies the public declarations used as affected surfaces in this report. | `P4-API-04`, `P4-API-06` |
| `go test ./go-llm-gateway/pkg/gateway -run 'TestInteract_(NormalizesProviderError|EmitsCancellationWhenContextCancelledBeforeProviderReturns|PreservesPartialOutputBeforeCancellation)'` | Interaction events preserve structured provider error, timeout, caller cancellation, and partial-output behavior. | Tests pass without credentials, proving structured event evidence while not proving a typed `errors.Is` / `errors.As` taxonomy. | `P4-API-02`, `P4-API-05` |
| `go test ./go-llm-gateway/pkg/testing -run 'TestSessionReplayer_(FailsOnUnexpectedOutboundEvent|FailsWhenExpectedOutboundIsOmitted|StopsDeliveryWhenOwnedContextCanceled)'` | Replay divergence and replay cancellation paths are deterministic and credential-free. | Tests pass, proving explicit replay mismatch/cancellation evidence while leaving typed replay error classification open. | `P4-API-02`, `P4-API-05` |
| `go test ./go-llm-gateway/pkg/inference -run 'TestInferStream_PassthroughAllFields|TestSessionGatewayInferencer_ConnectSession'` | Gateway inferencer adapters pass request/session fields through to providers rather than applying local unsupported-feature validation. | Tests pass, confirming passthrough behavior that supports the `P4-API-06` failure evidence. | `P4-API-06` |
| `go test ./go-llm-gateway/pkg/providers/fal -run 'TestFalProvider_(Infer_InvalidRequests|InferStream_ReturnsClosedChannel)'` | fal has provider-local invalid-request behavior and closed streaming behavior, not a shared public unsupported-feature validation contract. | Tests pass without live fal credentials or network access. | `P4-API-04`, `P4-API-06` |
| `go test ./go-llm-gateway/pkg/providers/openai -run 'TestConnectSession_(MissingAPIKeyFailsBeforeDial|MissingDialerFailsBeforeDial)|TestApplyInferenceRequestOptions_ThinkingIgnored'` | OpenAI session preconditions and ignored thinking options are local provider behaviors, not shared capability-backed validation. | Tests pass without dialing a live provider. | `P4-API-06`, `P4-API-07` |
| `go test ./go-llm-gateway/pkg/providers/grok -run 'TestConnectSession_MissingDialerFailsBeforeDial'` | Grok session connection preconditions fail locally before websocket dialing. | Test passes without live provider credentials or network access. | `P4-API-06`, `P4-API-07` |
| `go test ./go-llm-gateway/pkg/...` | The gateway package tests, provider tests, docs examples compiled by Go, and public package tests remain deterministic for this validator slice. | All package tests pass locally. | all gateway rows |
| `make typecheck` | Workspace packages compile from the documented root without live credentials. | Command exits successfully. | quality gate |
| `make test` | Deterministic workspace tests pass from the documented root. | Command exits successfully. | quality gate |
| `make lint` | Workspace lint passes from the documented root using the configured lint tool. | Command exits successfully, or reports the configured missing-tool guidance if the reviewer has not installed `golangci-lint`. | quality gate |

The command set intentionally separates repository-inspection commands from Go
test commands. Inspection commands prove public declarations, docs, and audit
coverage gaps. Go test commands prove current observable runtime behavior and
also make clear where passing tests demonstrate a gap, such as passthrough
without local validation, rather than row closure.

## Final Closure Decisions

No reviewed Phase 4 checklist row may close from the current starter evidence.
The next planner action is exactly: `repair`.

The planner must consume this validator report before queueing additional
Phase 4 implementation work. The current evidence shows useful starter
progress, but the public API contract is not ready for cleanup/reconciliation
or the next Phase 4 feature batch because typed error taxonomy, provider
capability discovery, local unsupported-feature validation, stream semantics,
and row-to-audit mapping remain incomplete.

| Row | Outcome | Closure decision | Evidence summary | Affected files / declarations | Exact repair work |
| --- | --- | --- | --- | --- | --- |
| `P4-API-01` | `uncertain` | `must remain open` | Audit findings cover session context and replay relay cancellation, but they do not map timeout/cancellation behavior across every blocking gateway, loop, provider, docs, tests, and example surface. | `go-agent-loop/pkg/messages.SessionInferencer`; `go-llm-gateway/pkg/inference.SessionGatewayInferencer`; `agent-cli/internal/services/session.go`; `go-llm-gateway/pkg/testing.SessionRecorder`; `go-llm-gateway/pkg/testing.SessionReplayer` | Add explicit `P4-API-01` audit mapping for all blocking and provider entrypoints, separate already-repaired relay cancellation from remaining session request-shape work, and attach docs/tests/examples evidence for caller-owned lifetime behavior. |
| `P4-API-02` | `fail` | `must remain open` | Interaction events expose string-coded structured fields, but main gateway, provider, replay, validation, and cancellation paths do not expose a public typed taxonomy usable with `errors.Is` or `errors.As`. | `go-llm-gateway/pkg/gateway.DefaultGateway.Infer`; `DefaultGateway.InferStream`; `DefaultGateway.Interact`; `InteractionError`; `InteractionCancellation`; `go-llm-gateway/pkg/testing.SessionReplayer.Err`; `ReplayWebSocketDialer.Err`; `go-agent-loop/pkg/messages.ErrorValue`; public docs | Introduce additive typed gateway error classes or helpers, preserve classes through representative stateless, stream, replay, validation, and cancellation paths, document caller branching guidance, and add credential-free `errors.Is` / `errors.As` tests. |
| `P4-API-03` | `uncertain` | `must remain open` | Audit findings identify lifecycle and result ambiguity, but row-level evidence does not yet cover all public result values and stream events for success, partial success, terminal failure, replay divergence, cancellation, and provider rejection. | `go-agent-loop/pkg/participants.ModelRunner`; `agent-cli/internal/services/session.go`; `go-agent-loop/pkg/messages.Message`; `go-agent-loop/pkg/messages.StreamMessage`; `go-llm-gateway/pkg/models`; gateway result surfaces | Add explicit `P4-API-03` mapping for public result and stream-event declarations, define terminal state semantics, and split documentation-only repairs from implementation repairs that require fixture or CLI replay updates. |
| `P4-API-04` | `fail` | `must remain open` | Consumers cannot query provider capabilities through public gateway/provider APIs; the README has static provider notes but no runtime capability model with supported, unsupported, and unknown states. | `go-llm-gateway/pkg/providers.Provider`; `SessionProvider`; `InferenceRequest`; `go-llm-gateway/pkg/gateway.Gateway`; `DefaultGateway`; `DefaultSessionGateway`; provider constructors; `go-llm-gateway/README.md` | Add a public capability contract, cover tools, streaming, sessions, audio, image input, video output, reasoning, prompt caching, and provider-specific config, wire representative providers, document the API, and add credential-free discovery tests. |
| `P4-API-05` | `uncertain` | `must remain open` | Interaction cancellation and partial-output behavior are tested, but direct stream errors and replay mismatch paths are not tied to typed or documented taxonomy classes. | `go-llm-gateway/pkg/gateway.DefaultGateway.InferStream`; `DefaultGateway.Interact`; `InteractionEvent`; `go-agent-loop/pkg/messages.ErrorValue`; `go-llm-gateway/pkg/testing.SessionReplayer.Err`; `ReplayWebSocketDialer.Err` | Define stream error mapping across direct streams, interaction events, provider adapters, and replay helpers; preserve replay divergence and cancellation as typed or documented structured classes; add docs and deterministic tests for partial success and terminal failure cases. |
| `P4-API-06` | `fail` | `must remain open` | Gateway and session paths forward requests to providers without shared local unsupported-feature validation; provider-specific behavior is inconsistent and not inspectable as one public validation contract. | `go-llm-gateway/pkg/gateway.DefaultGateway.Infer`; `DefaultGateway.InferStream`; `DefaultSessionGateway.ConnectSession`; `gateway.InferenceRequest`; `providers.InferenceRequest`; `models.SessionConfig`; provider option and session implementations | Add capability-backed validation before provider execution for stateless and session requests, return structured or typed validation errors naming provider, requested feature or mode, and capability state, settle fal streaming behavior, and prove no provider side effects occur before rejection. |
| `P4-API-07` | `uncertain` | `must remain open` | Earlier dependency-injection repairs are useful, but hidden prompt-resolution side effects and Phase 4 row-level closure decisions are still incomplete across public constructors and composition seams. | `go-agent-loop/pkg/agentloop.New`; `agent-cli/internal/agent.buildProviderHTTPRuntime`; `agent-cli/internal/services/session_runtime.go`; `agent-cli/internal/agent.Executor.loadSystemPrompt`; OpenAI and Grok provider runtime seams | Add explicit `P4-API-07` mapping that separates closed prerequisite DI repairs from remaining hidden side effects, name user-facing construction seams, and identify compatibility-sensitive ownership changes. |
| `P4-GATE-01` | `fail` | `must remain open` | Multiple row-level failures and uncertainties remain; the starter slices do not yet provide enough evidence, docs, examples, and credential-free commands to close the public API hardening gate. | `docs/internal/phase-4-api-contract-validator.md`; `docs/architecture/contract-gap-audit.md`; `docs/internal/checklist.md`; public package docs, tests, and examples | Queue the repair batches below, update the audit and public guidance with row-level evidence, rerun this validator after repairs, and do not queue the next Phase 4 feature batch until this gate can close or a new validator report supersedes this one. |

## Repair Batches

1. Audit reconciliation batch: update `docs/architecture/contract-gap-audit.md`
   so every `P4-API-*` row names affected public packages, exported
   declarations, observable contract issues, docs/tests/examples evidence, and
   implementation-ready repair slices. This batch should close no
   implementation row by itself unless it also cites repaired public behavior.
2. Typed error and stream semantics batch: introduce the public error taxonomy
   and preservation tests for gateway, provider, replay, validation, direct
   streaming, interaction events, cancellation, and partial-output paths.
   Update public docs so callers know when to use `errors.Is`, `errors.As`, or
   documented event fields.
3. Provider capability and local validation batch: add the public capability
   contract first, then use it to reject unsupported stateless and session
   request features locally with inspectable errors before provider execution.
   Include public docs, examples, and credential-free tests.
4. Dependency and result contract batch: finish row-level decisions for public
   constructors, provider runtime ownership, prompt resolution side effects,
   result values, and terminal stream states. Keep compatibility-sensitive
   changes additive or explicitly staged.

## Current Story Status

Stories 001, 002, 003, 004, 005, and 006 are complete. This validator report
is ready for planner consumption, and the report's next planner action is
`repair`.
