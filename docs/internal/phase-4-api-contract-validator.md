# Phase 4 API Contract Convergence Validator

## Subject Under Review

This validator reviews the post-batch-017 Phase 4 public API contract hardening
baseline. Run this pass only after the repair slices under review have landed
and the branch under review is intended to represent the candidate baseline for
the next Phase 4 planning decision.

The validator inspects observable repository state. It does not implement new
Phase 4 API features and does not use broad cleanup or duplicate CI coverage as
a substitute for API contract evidence.

## Baseline Under Review

The candidate baseline under review is the combined current repository evidence
from these Phase 4 repair lanes:

1. Audit/provenance reconciliation across `docs/architecture/contract-gap-audit.md`,
   prior validator findings, checklist rows, public docs, deterministic tests,
   and batch 017 repair evidence.
2. Typed error and stream terminal repair across gateway, provider, loop,
   session, replay, cancellation, partial-output, and terminal-failure paths.
3. Provider capability and local validation repair across public capability
   discovery, supported/unsupported/unknown states, and unsupported-feature
   rejection before provider execution.
4. Dependency/result/context/lifecycle repair across caller-owned contexts,
   public result contracts, stream/session lifecycle states, constructor seams,
   injected dependencies, and hidden side-effect boundaries.

## Checklist Rows Under Review

This validator records findings for exactly these `docs/internal/checklist.md`
rows. The row text below is the authoritative closure target, not a summary:

| Row | Required outcome from `docs/internal/checklist.md` | Primary evidence surfaces |
| --- | --- | --- |
| `P4-API-01` | Public APIs that can block or perform provider work expose caller-controlled context, cancellation, and timeout behavior clearly enough for consumers to own request lifetime. | `go-agent-loop/pkg/agentloop`, `go-agent-loop/pkg/engine`, `go-llm-gateway/pkg/gateway`, `go-llm-gateway/pkg/inference`, provider packages, public docs, tests, examples |
| `P4-API-02` | Public gateway, provider, replay, validation, and cancellation failures preserve typed or structured classifications so callers can branch with `errors.Is`, `errors.As`, or documented fields instead of parsing strings. | `go-llm-gateway/pkg/gateway`, `go-llm-gateway/pkg/testing`, provider packages, `agent-cli/internal/services`, public docs, tests, examples |
| `P4-API-03` | Public result values and stream events make success, partial success, terminal failure, replay divergence, cancellation, and provider rejection unambiguous. | `go-agent-loop/pkg/agentloop`, `go-agent-loop/pkg/subsystems`, `go-llm-gateway/pkg/gateway`, `go-llm-gateway/pkg/models`, public docs, tests, examples |
| `P4-API-04` | Consumers can discover provider capabilities through public `go-llm-gateway` APIs without importing `go-agent-loop` runtime internals or concrete provider internals. | `go-llm-gateway/pkg/providers`, `go-llm-gateway/pkg/gateway`, provider docs, examples, tests |
| `P4-API-05` | Streaming and session APIs document and preserve completion, cancellation, replay mismatch, provider-close, and error-classification semantics across provider and gateway boundaries. | `go-agent-loop/pkg/subsystems`, `go-llm-gateway/pkg/gateway`, `go-llm-gateway/pkg/inference`, `go-llm-gateway/pkg/testing`, provider stream adapters, public docs, tests |
| `P4-API-06` | Unsupported provider/request features fail locally before provider execution, with inspectable errors that identify the provider, requested feature or mode, and capability state. | `go-llm-gateway/pkg/providers`, `go-llm-gateway/pkg/gateway`, provider validation tests, public docs, examples |
| `P4-API-07` | Public constructors and composition seams keep filesystem, environment, process, network, transport, time, and provider runtime dependencies caller-owned or explicitly injected instead of hidden behind defaults. | `agent-cli/internal/agent`, `agent-cli/internal/services`, `go-agent-loop/pkg/agentloop`, `go-llm-gateway/pkg/providers`, `docs/architecture/dependencies.md`, tests |
| `P4-GATE-01` | The completed Phase 4 starter slices have reviewer-verifiable evidence, docs, examples, and credential-free local commands sufficient for planners to decide whether to repair, reconcile, or queue the next Phase 4 feature batch. | `docs/internal/phase-4-api-contract-validator.md`, `docs/architecture/contract-gap-audit.md`, public package docs, tests, examples, root `Makefile` quality targets |

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

CI, lint, typecheck, and test success are quality gates for the reviewed
repository state, but command success alone is not sufficient row closure
without public contract evidence tied to the checklist row.

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

- `outcome`: `uncertain`
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
  - Runtime implementation evidence now partially satisfies this row:
    `go-llm-gateway/pkg/capabilities` defines the public capability model,
    `providers.CapabilityReporter` and `gateway.CapabilityReporter` expose
    discovery interfaces, and `DefaultGateway.Capabilities` plus
    `DefaultSessionGateway.Capabilities` let consumers query the configured
    provider before sending a request.
  - The row remains open because concrete provider coverage is not yet complete
    across all provider families, and the audit still contains stale absence
    wording that should be reconciled with the implemented API before closure.
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
  - Reconcile `P4-CAP-01` with the implemented provider-neutral capability
    model, gateway-level discovery methods, public docs, and tests.
  - Verify concrete provider coverage for supported, unsupported, and unknown
    semantics across tools, streaming, sessions, audio, image input, video
    output, reasoning, prompt caching, provider config, and provider-specific
    limits.
  - Add or keep credential-free tests that prove capability discovery through
    public gateway/provider APIs for representative concrete providers.
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

- `outcome`: `uncertain`
- `evidence`:
  - `P4-VALIDATION-01` maps unsupported request feature behavior to
    `P4-API-06`. Its audit finding is now stale for the gateway/session seam:
    `DefaultGateway` validates stateless requests before provider dispatch,
    `DefaultSessionGateway` validates sessions before provider connection, and
    unsupported local rejections use the public `UnsupportedFeatureError`
    contract.
  - `P4-VALIDATION-01` identifies inconsistent provider behavior: unsupported
    fields may be ignored, translated to provider-specific params, returned as
    formatted string errors, or represented by fal's immediately closed stream
    channel.
  - `P4-CAP-01` supplies the required capability model prerequisite, and
    `P4-DI-02` records session pre-dial validation gaps for realtime
    modalities, audio formats, turn detection, tools, sample rates, and raw
    config.
  - Runtime implementation evidence makes this row uncertain rather than failed:
    gateway/session validation now reports provider, requested feature or mode,
    and capability state before provider execution or connection, while closure
    still depends on audit reconciliation, concrete provider coverage,
    interaction/inferencer validation seam evidence, fal streaming behavior, and
    docs/examples.
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
  - Reconcile `P4-VALIDATION-01` with the implemented gateway/session
    validation seam and `UnsupportedFeatureError` contract.
  - Extend validation closure evidence through interaction gateways and
    `GatewayInferencer`, or document why those seams intentionally rely on
    `DefaultGateway` validation.
  - Complete concrete provider capability reporting so unsupported behavior does
    not silently degrade to unknown for providers whose unsupported features are
    already known.
  - Decide and document the fal streaming behavior against the public capability
    contract.
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
    `P4-VALIDATION-01`, and `P4-DI-02`. `P4-API-04` and `P4-API-06` are now
    uncertain rather than failed because implementation, public docs, and
    credential-free behavioral tests exist, but concrete provider coverage,
    interaction/inferencer seams, fal streaming behavior, and stale audit
    wording still need reconciliation before closure.
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

The current repository now exposes a public typed gateway error taxonomy through
`go-llm-gateway/pkg/gateway/errors.go`, including exported error classes,
`GatewayError`, provider HTTP status classification, replay mismatch, and
cancellation helpers. The evidence is still partial: public README guidance
documents additive coverage, while several provider and session surfaces remain
best-effort or provider-specific.

### `P4-API-02` - Typed caller-actionable errors

- `outcome`: `uncertain`
- `evidence`:
  - `go-llm-gateway/pkg/gateway/errors.go` defines public classes including
    `ErrAuthentication`, `ErrAuthorization`, `ErrRateLimit`,
    `ErrInvalidRequest`, `ErrUnsupportedModel`, `ErrProviderHTTPStatus`,
    `ErrTransport`, `ErrReplayMismatch`, and `ErrCancellation`.
    `GatewayError.Is` supports `errors.Is`, and
    `ProviderHTTPStatusError` supports `errors.As` for provider, status, and
    body details.
  - Gateway and provider tests now prove representative typed classification
    for provider HTTP status, transport, stream error events, replay mismatch,
    and cancellation paths. The README includes caller guidance for
    `errors.Is` / `errors.As` and documents current additive classification
    limits.
  - `go-llm-gateway/pkg/gateway.Interact` emits structured
    `InteractionError` values with `Code`, `Message`, `Retryable`, and
    `Details` fields. Tests prove the provider error path emits
    `provider_error`, deadline errors emit `provider_timeout` with
    `Retryable: true`, and pre-provider cancellation emits
    `InteractionCancellation` with reason `caller_cancelled`.
  - The row remains open because classification is not yet uniform across all
    provider adapters and session surfaces. Public docs explicitly state that
    Anthropic, Gemini, fal.ai, and session provider surfaces may still return
    provider-specific or generic errors where typed gateway classification has
    not been wired.
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
  - Reconcile `P4-ERR-01`, `P4-ERR-02`, and `P4-ERR-03` in the audit so stale
    absence claims are replaced with the current typed taxonomy and the precise
    remaining provider/session coverage gaps.
  - Finish wrapping or translating the remaining Anthropic, Gemini, fal.ai,
    session, and validation paths so representative public surfaces preserve the
    intended class without forcing callers to parse `err.Error()` or string
    event codes.
  - Add or extend credential-free tests where classification is still best
    effort, especially session provider errors and any provider adapters not yet
    covered by `errors.Is` / `errors.As` assertions.
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
    now use `gateway.ErrReplayMismatch` and are tested with `errors.Is`.
    Remaining uncertainty is concentrated in end-to-end stream taxonomy
    preservation across provider adapters, serialized payloads, and session
    surfaces.
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
  - Preserve typed error classes consistently through all stream consumers,
    including provider adapters, serialized error payload boundaries, and
    session surfaces.
  - Add public docs and credential-free tests that distinguish partial success,
    terminal failure, cancellation, replay mismatch, and provider rejection.
- `reviewer commands`:
  - `go test ./go-llm-gateway/pkg/gateway -run 'TestInteract_PreservesPartialOutputBeforeCancellation'`
  - `go test ./go-llm-gateway/pkg/testing -run 'TestSessionReplayer_(FailsOnUnexpectedOutboundEvent|StopsDeliveryWhenOwnedContextCanceled)'`
  - `rg -n "StreamTypeError|NewErrorValue|ErrorValue|replay divergence|caller_cancelled" go-agent-loop/pkg/messages go-llm-gateway/pkg go-llm-gateway/README.md`

## Gateway Error Taxonomy Closure Summary

`P4-API-02` is uncertain rather than failed: the public typed taxonomy and
several preservation tests now exist, but coverage is explicitly additive and
not yet uniform across every provider, validation, replay, direct stream, and
session path. `P4-API-05` remains uncertain because interaction cancellation,
partial-output behavior, and some stream error classifications are tested, while
serialized stream payloads and remaining provider/session paths still need a
clear documented mapping to the taxonomy.

## Provider Capability and Validation Evidence

This section validates whether the completed provider capability discovery and
local request validation slice is present in public, consumer-usable
`go-llm-gateway` APIs. The current repository now exposes a runtime capability
contract, supported/unsupported/unknown semantics, gateway/session discovery
methods, structured `UnsupportedFeatureError` values, README guidance, and
credential-free gateway tests. Remaining uncertainty is about completeness
across concrete provider families, interaction/inferencer seams, and stale audit
wording that has not yet been reconciled with the implementation.

### `P4-API-04` - Provider capability discovery

- `outcome`: `uncertain`
- `evidence`:
  - `go-llm-gateway/pkg/capabilities.ProviderCapabilities` exposes stateless
    and session capability fields for tools, streaming, sessions, audio, image
    input, video output, reasoning, prompt caching, and provider-specific
    config with explicit `supported`, `unsupported`, and `unknown` states.
  - `providers.CapabilityReporter`, `providers.UnknownProviderCapabilities`,
    `gateway.CapabilityReporter`, `DefaultGateway.Capabilities`, and
    `DefaultSessionGateway.Capabilities` let consumers inspect capabilities
    through public provider or gateway APIs without importing concrete provider
    internals.
  - README guidance documents the capability API, unknown fallback semantics,
    field mapping, and local validation behavior. Gateway tests prove discovery
    for fake stateless/session providers and prove unknown fallback behavior
    without provider execution.
  - The row remains open because current concrete-provider evidence is narrower
    than the row: OpenAI reports capabilities, but the validator did not find
    equivalent concrete capability reporter tests for every provider family
    named in the gateway README, and `P4-CAP-01` in the audit still describes
    capability discovery as absent.
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
  - Reconcile `P4-CAP-01` in `docs/architecture/contract-gap-audit.md` with
    the implemented capability package, gateway/provider aliases, discovery
    methods, README guidance, and tests.
  - Add or verify concrete provider capability reporters and credential-free
    tests across Anthropic, Gemini, fal.ai, Grok/session, and any other provider
    family where current public behavior still falls back to unknown.
  - Record provider-specific limits precisely so closure does not overclaim
    support for media, reasoning, prompt caching, sessions, or raw config.
- `reviewer commands`:
  - `rg -n "type Provider interface|type SessionProvider interface|type Gateway interface|type InferenceRequest struct|type SessionConfig struct" go-llm-gateway/pkg/providers go-llm-gateway/pkg/gateway go-llm-gateway/pkg/models`
  - `rg -n "Capability|Capabilities|capability|capabilities" go-llm-gateway/pkg/providers go-llm-gateway/pkg/gateway go-llm-gateway/README.md`
  - `sed -n '172,198p' go-llm-gateway/README.md`

### `P4-API-06` - Local unsupported-feature validation

- `outcome`: `uncertain`
- `evidence`:
  - `DefaultGateway.Infer` and `DefaultGateway.InferStream` now call
    `validateStatelessRequest` before provider dispatch. The validator covers
    tools, streaming, image input, audio input/output, video output, reasoning,
    prompt caching, and raw provider-specific config when the relevant
    capability is explicitly unsupported.
  - `DefaultSessionGateway.ConnectSession` now calls `validateSessionConfig`
    before provider connection. The validator covers unsupported sessions,
    session tools, audio input/output config, and raw provider-specific config.
  - Local validation returns `UnsupportedFeatureError` with provider, feature,
    requested mode, and capability state, and tests prove unsupported stateless
    and session features are rejected before provider execution or connection.
  - Unknown capabilities are intentionally not rejected locally. That fallback is
    documented, but it means unsupported behavior remains dependent on complete
    provider capability reporting. The row therefore remains open until
    concrete provider coverage and stale audit wording are reconciled.
  - Some surfaces still need explicit closure decisions: interaction gateways
    and `GatewayInferencer` should be checked against the same validation
    contract, fal streaming behavior should be aligned with capabilities, and
    provider-specific/session errors should be audited for typed classification.
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
  - Reconcile `P4-VALIDATION-01` with the implemented gateway/session
    validation seam and `UnsupportedFeatureError` contract.
  - Extend validation closure evidence through interaction gateways and
    `GatewayInferencer`, or document why those seams intentionally rely on
    `DefaultGateway` validation.
  - Complete concrete provider capability reporting so unsupported behavior does
    not silently degrade to unknown for providers whose unsupported features are
    already known.
  - Decide and document the fal streaming behavior against the public capability
    contract.
  - Add or keep credential-free tests proving unsupported features are rejected
    before HTTP, SDK, websocket, or provider execution side effects across the
    remaining representative provider paths.
- `reviewer commands`:
  - `sed -n '28,66p' go-llm-gateway/pkg/gateway/gateway.go`
  - `sed -n '51,53p' go-llm-gateway/pkg/gateway/session_gateway.go`
  - `go test ./go-llm-gateway/pkg/inference -run 'TestInferStream_PassthroughAllFields|TestSessionGatewayInferencer_ConnectSession'`
  - `go test ./go-llm-gateway/pkg/providers/fal -run 'TestFalProvider_(Infer_InvalidRequests|InferStream_ReturnsClosedChannel)'`
  - `go test ./go-llm-gateway/pkg/providers/openai -run 'TestConnectSession_(MissingAPIKeyFailsBeforeDial|MissingDialerFailsBeforeDial)|TestApplyInferenceRequestOptions_ThinkingIgnored'`
  - `go test ./go-llm-gateway/pkg/providers/grok -run 'TestConnectSession_MissingDialerFailsBeforeDial'`

## Provider Capability and Validation Closure Summary

`P4-API-04` and `P4-API-06` are uncertain rather than failed. The repository now
has a public runtime capability API, supported/unsupported/unknown semantics,
gateway and session discovery methods, structured local unsupported-feature
validation, README guidance, and credential-free gateway tests. The rows must
remain open until the audit is reconciled with this implementation and concrete
provider coverage, interaction/inferencer validation seams, fal streaming
behavior, docs, examples, and provider-specific tests are complete enough for
row closure.

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
| `rg -n "capabil|Capability|Capabilities" docs/architecture/contract-gap-audit.md go-llm-gateway/pkg go-llm-gateway/README.md` | Capability discovery is covered by the audit and now exists as a public runtime provider/gateway capability contract, while audit wording and concrete provider coverage still need reconciliation. | Output shows `P4-CAP-01` audit evidence plus `pkg/capabilities`, `providers.CapabilityReporter`, `gateway.CapabilityReporter`, gateway `Capabilities()` methods, README guidance, and current provider coverage. | `P4-API-04`, `P4-GATE-01` |
| `rg -n "UnsupportedFeatureError|validateStatelessRequest|validateSessionConfig|unsupported|validation|capabil|feature" docs/architecture/contract-gap-audit.md go-llm-gateway/pkg go-llm-gateway/README.md` | Unsupported-feature validation is covered by the audit and now implemented for gateway stateless/session paths when capabilities explicitly report unsupported features. | Output shows `P4-VALIDATION-01` audit evidence, `UnsupportedFeatureError`, gateway validation functions, README guidance, and remaining provider/inferencer surfaces that still need closure decisions. | `P4-API-06`, `P4-GATE-01` |
| `rg -n "type Provider interface|type SessionProvider interface|type Gateway interface|type InferenceRequest struct|type SessionConfig struct" go-llm-gateway/pkg/providers go-llm-gateway/pkg/gateway go-llm-gateway/pkg/models` | The public provider, gateway, request, and session declarations are inspectable without importing concrete provider internals. | Output identifies the public declarations used as affected surfaces in this report. | `P4-API-04`, `P4-API-06` |
| `go test ./go-llm-gateway/pkg/gateway -run 'Test(GatewayError|ProviderHTTPStatusError|ReplayMismatchError|CancellationError|Infer_|InferStream_|GatewayCapabilities|SessionGateway|GatewayRejectsUnsupported|GatewayAllowsUnknown|Interact_)'` | Gateway typed errors, stream error events, capability discovery, unsupported-feature validation, unknown fallback behavior, and interaction cancellation are all credential-free and observable. | Tests pass, proving current implemented evidence while leaving remaining provider/session coverage and audit reconciliation as row-closure gaps. | `P4-API-02`, `P4-API-04`, `P4-API-05`, `P4-API-06` |
| `go test ./go-llm-gateway/pkg/testing -run 'TestSessionReplayer_(FailsOnUnexpectedOutboundEvent|FailsWhenExpectedOutboundIsOmitted|StopsDeliveryWhenOwnedContextCanceled)'` | Replay divergence and replay cancellation paths are deterministic and credential-free. | Tests pass, proving replay mismatch now matches `gateway.ErrReplayMismatch` where divergence is expected. | `P4-API-02`, `P4-API-05` |
| `go test ./go-llm-gateway/pkg/inference -run 'TestInferStream_PassthroughAllFields|TestSessionGatewayInferencer_ConnectSession'` | Gateway inferencer adapters remain observable seams that should be checked against the gateway validation and taxonomy contracts. | Tests pass, proving current adapter behavior without live credentials. | `P4-API-06` |
| `go test ./go-llm-gateway/pkg/providers/fal -run 'TestFalProvider_(Infer_InvalidRequests|InferStream_ReturnsClosedChannel)'` | fal has provider-local invalid-request behavior and closed streaming behavior that still needs an explicit closure decision against the public capability contract. | Tests pass without live fal credentials or network access. | `P4-API-04`, `P4-API-06` |
| `go test ./go-llm-gateway/pkg/providers/openai -run 'Test(OpenAIProviderCapabilities|ConnectSession_|ApplyInferenceRequestOptions_|Replay_)'` | OpenAI has concrete capability and typed error evidence, plus session precondition tests that run without live provider access. | Tests pass without dialing a live provider. | `P4-API-02`, `P4-API-04`, `P4-API-06`, `P4-API-07` |
| `go test ./go-llm-gateway/pkg/providers/grok -run 'TestConnectSession_MissingDialerFailsBeforeDial'` | Grok session connection preconditions fail locally before websocket dialing. | Test passes without live provider credentials or network access. | `P4-API-06`, `P4-API-07` |
| `go test ./go-llm-gateway/pkg/...` | The gateway package tests, provider tests, docs examples compiled by Go, and public package tests remain deterministic for this validator slice. | All package tests pass locally. | all gateway rows |
| `make typecheck` | Workspace packages compile from the documented root without live credentials. | Command exits successfully. | quality gate |
| `make test` | Deterministic workspace tests pass from the documented root. | Command exits successfully. | quality gate |
| `make lint` | Workspace lint passes from the documented root using the configured lint tool. | Command exits successfully, or reports the configured missing-tool guidance if the reviewer has not installed `golangci-lint`. | quality gate |

The command set intentionally separates repository-inspection commands from Go
test commands. Inspection commands prove public declarations, docs, implemented
capability/error/validation surfaces, and audit drift. Go test commands prove
current observable runtime behavior and make clear where passing tests are
implementation evidence that still needs provider-coverage or audit
reconciliation before row closure.

## Final Closure Decisions

No reviewed Phase 4 checklist row may close from the current starter evidence.
The next planner action is exactly: `repair`.

The planner must consume this validator report before queueing additional
Phase 4 implementation work. The current evidence shows useful starter
progress, including implemented typed error, capability, and local validation
surfaces, but the public API contract is not ready for cleanup/reconciliation
or the next Phase 4 feature batch because coverage, provider-specific behavior,
stream/session semantics, docs/examples, and row-to-audit reconciliation remain
incomplete.

| Row | Outcome | Closure decision | Evidence summary | Affected files / declarations | Exact repair work |
| --- | --- | --- | --- | --- | --- |
| `P4-API-01` | `uncertain` | `must remain open` | Audit findings cover session context and replay relay cancellation, but they do not map timeout/cancellation behavior across every blocking gateway, loop, provider, docs, tests, and example surface. | `go-agent-loop/pkg/messages.SessionInferencer`; `go-llm-gateway/pkg/inference.SessionGatewayInferencer`; `agent-cli/internal/services/session.go`; `go-llm-gateway/pkg/testing.SessionRecorder`; `go-llm-gateway/pkg/testing.SessionReplayer` | Add explicit `P4-API-01` audit mapping for all blocking and provider entrypoints, separate already-repaired relay cancellation from remaining session request-shape work, and attach docs/tests/examples evidence for caller-owned lifetime behavior. |
| `P4-API-02` | `uncertain` | `must remain open` | Public gateway error classes, typed wrappers, README guidance, and representative `errors.Is` / `errors.As` tests exist, but classification is explicitly additive and not yet uniform across all provider, validation, direct stream, and session surfaces. | `go-llm-gateway/pkg/gateway/errors.go`; `DefaultGateway.Infer`; `DefaultGateway.InferStream`; `DefaultGateway.Interact`; `InteractionError`; `InteractionCancellation`; `go-llm-gateway/pkg/testing.SessionReplayer.Err`; `ReplayWebSocketDialer.Err`; `go-agent-loop/pkg/messages.ErrorValue`; public docs | Reconcile stale audit findings with the implemented taxonomy, finish classification on remaining provider/session/validation paths, and keep credential-free tests for every representative public error class. |
| `P4-API-03` | `uncertain` | `must remain open` | Audit findings identify lifecycle and result ambiguity, but row-level evidence does not yet cover all public result values and stream events for success, partial success, terminal failure, replay divergence, cancellation, and provider rejection. | `go-agent-loop/pkg/participants.ModelRunner`; `agent-cli/internal/services/session.go`; `go-agent-loop/pkg/messages.Message`; `go-agent-loop/pkg/messages.StreamMessage`; `go-llm-gateway/pkg/models`; gateway result surfaces | Add explicit `P4-API-03` mapping for public result and stream-event declarations, define terminal state semantics, and split documentation-only repairs from implementation repairs that require fixture or CLI replay updates. |
| `P4-API-04` | `uncertain` | `must remain open` | Consumers can query a public capability contract with supported, unsupported, and unknown states through gateway/provider APIs, but concrete provider coverage and stale audit reconciliation are incomplete. | `go-llm-gateway/pkg/capabilities`; `providers.CapabilityReporter`; `gateway.CapabilityReporter`; `DefaultGateway.Capabilities`; `DefaultSessionGateway.Capabilities`; provider constructors; `go-llm-gateway/README.md` | Reconcile `P4-CAP-01` with the implemented API, complete or verify concrete provider capability reporters and tests, and avoid overclaiming support for provider-specific limits. |
| `P4-API-05` | `uncertain` | `must remain open` | Interaction cancellation, partial-output behavior, stream error classification, and replay mismatch typing have tests, but serialized stream payloads and remaining provider/session paths still need a clear taxonomy mapping. | `go-llm-gateway/pkg/gateway.DefaultGateway.InferStream`; `DefaultGateway.Interact`; `InteractionEvent`; `go-agent-loop/pkg/messages.ErrorValue`; `go-llm-gateway/pkg/testing.SessionReplayer.Err`; `ReplayWebSocketDialer.Err` | Define stream error mapping across direct streams, interaction events, provider adapters, serialized boundaries, and session helpers; add docs and deterministic tests for partial success and terminal failure cases. |
| `P4-API-06` | `uncertain` | `must remain open` | Gateway and session paths now reject explicitly unsupported capabilities before provider execution with `UnsupportedFeatureError`, but unknown fallback behavior, concrete provider coverage, interaction/inferencer seams, and fal streaming still need closure decisions. | `go-llm-gateway/pkg/gateway.DefaultGateway.Infer`; `DefaultGateway.InferStream`; `DefaultSessionGateway.ConnectSession`; `gateway.InferenceRequest`; `providers.InferenceRequest`; `models.SessionConfig`; `go-llm-gateway/pkg/capabilities.UnsupportedFeatureError`; provider option and session implementations | Reconcile `P4-VALIDATION-01`, extend validation evidence through remaining seams, complete provider capability reporting where unsupported behavior is known, settle fal streaming behavior, and prove no provider side effects occur before rejection on representative paths. |
| `P4-API-07` | `uncertain` | `must remain open` | Earlier dependency-injection repairs are useful, but hidden prompt-resolution side effects and Phase 4 row-level closure decisions are still incomplete across public constructors and composition seams. | `go-agent-loop/pkg/agentloop.New`; `agent-cli/internal/agent.buildProviderHTTPRuntime`; `agent-cli/internal/services/session_runtime.go`; `agent-cli/internal/agent.Executor.loadSystemPrompt`; OpenAI and Grok provider runtime seams | Add explicit `P4-API-07` mapping that separates closed prerequisite DI repairs from remaining hidden side effects, name user-facing construction seams, and identify compatibility-sensitive ownership changes. |
| `P4-GATE-01` | `fail` | `must remain open` | Multiple row-level uncertainties remain; the starter slices do not yet provide enough reconciled audit, provider-coverage, stream/session, docs, examples, and credential-free command evidence to close the public API hardening gate. | `docs/internal/phase-4-api-contract-validator.md`; `docs/architecture/contract-gap-audit.md`; `docs/internal/checklist.md`; public package docs, tests, and examples | Queue the repair batches below, update the audit and public guidance with row-level evidence, rerun this validator after repairs, and do not queue the next Phase 4 feature batch until this gate can close or a new validator report supersedes this one. |

## Repair Batches

1. Audit reconciliation batch: update `docs/architecture/contract-gap-audit.md`
   so every `P4-API-*` row names affected public packages, exported
   declarations, observable contract issues, docs/tests/examples evidence, and
   implementation-ready repair slices. This batch should close no
   implementation row by itself unless it also cites repaired public behavior.
2. Typed error and stream semantics batch: reconcile the audit with the
   implemented public taxonomy, then finish preservation tests for remaining
   provider, validation, direct streaming, session, interaction event,
   cancellation, and partial-output paths. Update public docs so callers know
   when to use `errors.Is`, `errors.As`, or documented event fields.
3. Provider capability and local validation batch: reconcile the audit with the
   implemented capability and validation contracts, complete concrete provider
   coverage where unsupported behavior is known, settle fal streaming behavior,
   and extend credential-free tests through remaining interaction/inferencer
   seams.
4. Dependency and result contract batch: finish row-level decisions for public
   constructors, provider runtime ownership, prompt resolution side effects,
   result values, and terminal stream states. Keep compatibility-sensitive
   changes additive or explicitly staged.

## Current Story Status

Stories 001, 002, 003, 004, 005, and 006 are complete. This validator report
is ready for planner consumption, and the report's next planner action is
`repair`.
