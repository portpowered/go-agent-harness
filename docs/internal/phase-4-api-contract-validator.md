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

The current audit at `docs/architecture/contract-gap-audit.md` distinguishes
several repair classes:

- must-fix contract defects: `CTX-01`, `ERR-01`, `ERR-02`,
  `LIFECYCLE-01`, `LIFECYCLE-02`
- dependency and ownership defects: `DI-03`, remaining watchpoints under
  `DI-04`, and regression-sensitive hidden coupling under `HC-03`
- documentation and naming defects: `DOC-01`, `DOC-02`
- compatibility-sensitive changes: `COMPAT-01`, `COMPAT-02`, `COMPAT-03`
- completed or narrowed prerequisite repairs: `DI-01`, `DI-02`, `DI-04`,
  `HC-03`, and `CTX-02` status notes from earlier Phase 2 slices

The audit does not yet contain explicit `P4-API-*` row labels, provider
capability discovery findings, or local unsupported-feature validation
findings. Those omissions are report-level evidence because this validator is
required to map every Phase 4 checklist row to affected public packages,
exported declarations, and observable contract issues.

### `P4-API-01` - Context and cancellation contracts

- `outcome`: `uncertain`
- `evidence`:
  - `CTX-01` records a context contract gap in
    `go-agent-loop/pkg/messages.SessionInferencer`,
    `go-llm-gateway/pkg/inference.SessionGatewayInferencer`, and
    `agent-cli/internal/services/session.go`: cancellation is explicit through
    `ConnectSession(ctx context.Context)`, but per-session request shape is
    split across constructor options and CLI/provider wiring.
  - `CTX-02` records that session replay and recording relay cancellation was
    narrowed by explicit relay lifecycle contexts.
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
  - `sed -n '85p' docs/internal/checklist.md`

### `P4-API-02` - Typed caller-actionable errors

- `outcome`: `uncertain`
- `evidence`:
  - `ERR-01` records that `go-agent-loop/pkg/messages.ErrorValue` has
    structured fields, but loop, participant, tool, and provider stream paths
    frequently collapse failures with `messages.NewErrorValue(err.Error())`.
  - `ERR-02` records that `agent-cli/internal/services/session.go` mixes
    transport, replay, provider, capture, and loop phases into wrapped text
    instead of one typed caller-actionable taxonomy.
  - `COMPAT-03` correctly classifies typed-error work as compatibility
    sensitive and recommends additive migration, which distinguishes must-fix
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
  - `sed -n '86p' docs/internal/checklist.md`

### `P4-API-03` - Result contracts and failure signals

- `outcome`: `uncertain`
- `evidence`:
  - `LIFECYCLE-01` records ambiguous session-open, response completion,
    provider close, and command stop boundaries across
    `go-agent-loop/pkg/participants.ModelRunner` and
    `agent-cli/internal/services/session.go`.
  - `LIFECYCLE-02` records that streaming inference completion rules differ
    between provider streams and loop-synthesized fallbacks, making provider
    completion versus normalization unclear.
  - `COMPAT-01` and `COMPAT-02` identify compatibility-sensitive impact on
    shared message/session types, CLI stop behavior, and persisted captures.
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
  - `sed -n '87p' docs/internal/checklist.md`

### `P4-API-04` - Provider capability discovery

- `outcome`: `fail`
- `evidence`:
  - The audit has no provider capability discovery finding that maps to
    `P4-API-04`.
  - The existing audit mentions provider-specific runtime selection in
    `HC-03`, `DI-02`, and `DI-04`, but those findings address construction and
    dependency ownership rather than public capability discovery.
  - No affected public provider capability declarations, capability fields, or
    consumer guidance are recorded in the audit.
- `affected files / declarations`:
  - missing from the audit for `go-llm-gateway/pkg/providers`
  - missing from the audit for `go-llm-gateway/pkg/gateway`
  - missing provider docs, examples, and tests mapping
- `closure decision`: `must remain open`
- `exact repair work`:
  - Add a dedicated `P4-API-04` audit finding for public provider capability
    discovery, including affected declarations and whether consumers can query
    capabilities without importing concrete provider internals.
  - Classify any missing capability fields or ambiguous supported,
    unsupported, and unknown semantics as must-fix contract defects rather than
    polish.
- `reviewer commands`:
  - `rg -n "capabil|Capability|Capabilities" docs/architecture/contract-gap-audit.md go-llm-gateway/pkg`
  - `sed -n '88p' docs/internal/checklist.md`

### `P4-API-05` - Stream semantics and error preservation

- `outcome`: `uncertain`
- `evidence`:
  - `ERR-01` covers stream error classification collapse into strings.
  - `LIFECYCLE-01` and `LIFECYCLE-02` cover session and streaming completion
    ambiguity, including provider-close and loop-synthesized boundaries.
  - `CTX-02` covers replay/record relay cancellation preservation after the
    prior repair.
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
  - `sed -n '89p' docs/internal/checklist.md`

### `P4-API-06` - Local unsupported-feature validation

- `outcome`: `fail`
- `evidence`:
  - The audit has no finding for local unsupported-feature validation before
    provider execution.
  - Existing provider runtime and session configuration findings describe
    hidden defaults or runtime ownership, but they do not record unsupported
    feature rejection, structured validation errors, provider identity,
    requested feature or mode, or capability state.
- `affected files / declarations`:
  - missing from the audit for `go-llm-gateway/pkg/providers`
  - missing from the audit for `go-llm-gateway/pkg/gateway`
  - missing provider validation tests, public docs, and examples mapping
- `closure decision`: `must remain open`
- `exact repair work`:
  - Add a dedicated `P4-API-06` audit finding for local validation of
    unsupported tools, streaming, sessions, audio, image input, video output,
    reasoning, prompt caching, and provider-specific config.
  - Record exact affected validation declarations and classify string-only or
    provider-live rejection behavior as must-fix contract defects.
- `reviewer commands`:
  - `rg -n "unsupported|validate|validation|capabil|feature" docs/architecture/contract-gap-audit.md go-llm-gateway/pkg`
  - `sed -n '90p' docs/internal/checklist.md`

### `P4-API-07` - Dependency injection and hidden side effects

- `outcome`: `uncertain`
- `evidence`:
  - `DI-01`, `DI-02`, and `DI-04` include completed or narrowed status notes
    for explicit tool execution decisions, stateless provider runtime wiring,
    and scoped session runtime wiring.
  - `DI-03` remains open for `agent-cli/internal/agent.Executor` prompt
    resolution because `loadSystemPrompt` mixes filesystem, workspace,
    config, and skills metadata side effects.
  - `HC-03` is marked resolved for scoped Grok and OpenAI record/replay paths
    and warns that provider-specific branching outside the session runtime seam
    should be treated as a regression.
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
  - `sed -n '91p' docs/internal/checklist.md`

### `P4-GATE-01` - Public API hardening gate readiness

- `outcome`: `fail`
- `evidence`:
  - Audit-backed coverage is incomplete for `P4-API-04` and `P4-API-06`.
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
  - Repair the exported API contract audit so it explicitly maps every Phase 4
    row to affected public packages, declarations, observable contract issues,
    closure blockers, and implementation-ready repair slices.
  - Do not queue the next Phase 4 feature batch until the missing capability
    discovery and local validation audit findings are added or this validator
    is updated with equivalent row-level evidence from public docs, tests, and
    examples.
- `reviewer commands`:
  - `sed -n '76,92p' docs/internal/checklist.md`
  - `sed -n '149,327p' docs/architecture/contract-gap-audit.md`

## Audit Coverage Closure Summary

No checklist row may close from audit evidence alone in this story. The audit
does provide useful must-fix and compatibility-sensitive evidence for
`P4-API-01`, `P4-API-02`, `P4-API-03`, `P4-API-05`, and `P4-API-07`, but each
of those rows remains open because the audit has not yet mapped every required
Phase 4 surface, affected declaration, public guidance surface, and exact
repair slice. `P4-API-04`, `P4-API-06`, and therefore `P4-GATE-01` fail audit
coverage because provider capability discovery and local unsupported-feature
validation are missing from the audit.

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

## Current Story Status

Stories 001, 002, 003, and 004 are complete. Later validator stories must fill
in reviewer-runnable command results, final row closure decisions, and the
final next planner action.
