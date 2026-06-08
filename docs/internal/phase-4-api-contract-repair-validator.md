# Phase 4 API Contract Repair Validator

## Subject Under Review

This validator reviews the candidate Phase 4 public API contract hardening
repair baseline. The baseline under review combines these repair lanes:

1. Audit reconciliation
2. Typed errors and stream repair
3. Provider capability discovery and local request validation repair
4. Dependency ownership, result contract, context, and lifecycle repair

The validator inspects the delivered repository state as an observable public
contract surface. It does not implement new feature behavior and does not close
rows from implementation intent alone.

## Checklist Rows Under Review

This report covers exactly these authoritative rows from
`docs/internal/checklist.md`:

| Checklist row | Required outcome cited from `docs/internal/checklist.md` |
| --- | --- |
| `P4-API-01` | Public blocking calls expose caller-controlled cancellation and timeout behavior anywhere they wait, perform external work, relay streams, replay fixtures, or flush recordings. |
| `P4-API-02` | Public gateway, provider, CLI, replay, and validation boundaries expose typed caller-actionable errors that support `errors.Is` or `errors.As` instead of requiring string parsing. |
| `P4-API-03` | Public result, buffer, session, and stream APIs expose unambiguous outcomes for empty success, cancellation, partial success, closed or drained state, and terminal failure. |
| `P4-API-04` | Public APIs expose provider capabilities for tools, streaming, sessions, audio, image input, video output, reasoning, prompt caching, and provider-specific configuration with supported, unsupported, and unknown states. |
| `P4-API-05` | Public streaming APIs preserve terminal events, cancellation, replay mismatch, partial output, and typed error details through observable stream events or documented result surfaces. |
| `P4-API-06` | Unsupported stateless and session request features fail locally before provider execution with inspectable errors identifying feature, provider, requested mode, and capability state. |
| `P4-API-07` | Constructors, provider runtime seams, prompt resolution, filesystem, environment, process, transport, network, and time dependencies are caller-owned, injected, side-effect free, or explicitly documented as open work. |
| `P4-GATE-01` | The Phase 4 public API contract hardening baseline is review-ready only when docs, examples where present, audit rows, public APIs, deterministic tests, and reviewer-runnable commands describe the same current contract. |

## Evidence Rules

Every row finding must be based on public, reviewer-verifiable evidence. Public
evidence can include exported declarations, public docs, examples where present,
deterministic tests, emitted events, CLI output contracts, or local commands
that require no live provider credentials.

CI success alone is not sufficient row closure. Typecheck, lint, and tests can
support a pass only when the cited command proves the specific public contract
under review. Docs or audit prose alone cannot close an implementation row
unless the row is explicitly documentation-only.

When evidence is absent, stale, contradictory, dependent on live credentials, or
not tied to a public contract surface, the row must be marked `fail` or
`uncertain` and must include exact future repair work.

## Row Finding Shape

Each row finding must use this shape:

### `[Checklist row]` - `[Area]`

- `verdict`: `pass` | `fail` | `uncertain`
- `closure decision`: `may mark complete` | `remains open`
- `public evidence`:
- `affected files / declarations`:
- `docs, examples, tests, audit, and API alignment`:
- `reviewer commands`:
- `exact repair work for non-pass rows`:

## Reviewer Commands

The final validator pass must cite exact commands next to each row. Commands
must be deterministic and must not require live credentials, external network
access, private local state, or hidden setup. The root quality commands available
for supporting evidence are:

```sh
make typecheck
make fmt
make vet
make test
make test-integration
make test-regressions
```

Those commands prove only their observable behavior. They do not, by themselves,
prove row closure without the row-specific public evidence above.

## Audit And Validator-015 Reconciliation

### Reconciliation Inputs

- `docs/architecture/contract-gap-audit.md`
- `docs/architecture/dependencies.md`
- `docs/internal/checklist.md`
- `docs/internal/phase-2-session-runtime-ownership-validator.md`
- public package docs and README files under `go-agent-loop`,
  `go-llm-gateway`, and `agent-cli`
- exported public API declarations under `go-agent-loop/pkg`,
  `go-llm-gateway/pkg`, and `agent-cli` CLI behavior docs

No committed validator-015 artifact is present in this checkout. Searches for
`validator 015`, `validator-015`, and `015` found only this PRD's reference to
the missing input plus unrelated numeric literals. Under the evidence rules for
this report, that absence is not a pass. It is an `uncertain` reconciliation
finding until the prior validator output is committed, linked from the PRD, or
explicitly superseded by a reviewer-facing cleanup note.

### Reconciled Audit Row Mapping

| Prior audit row | Current audit status | Phase 4 row mapping | Reconciliation decision |
| --- | --- | --- | --- |
| `CTX-01` | still open in `docs/architecture/contract-gap-audit.md` | `P4-API-01`, `P4-API-03`, `P4-API-07` | remains open because the session request/config contract is still split between constructor options, call-time context, and CLI helper logic. |
| `CTX-02` | narrowed after `phase-2-session-runtime-ownership-repair` | `P4-API-01`, `P4-API-07` | may count only as partial evidence for replay and recorder relay cancellation; it does not close all public blocking-call context ownership. |
| `ERR-01` | still open in `docs/architecture/contract-gap-audit.md` | `P4-API-02`, `P4-API-05` | remains open because shared stream errors still commonly cross module boundaries as `err.Error()` strings. |
| `ERR-02` | still open in `docs/architecture/contract-gap-audit.md` | `P4-API-02`, `P4-API-05` | remains open because CLI session command failures still lack one caller-actionable taxonomy for transport, replay, loop, provider, and capture phases. |
| `LIFECYCLE-01` | still open in `docs/architecture/contract-gap-audit.md` | `P4-API-03`, `P4-API-05` | remains open because session-open, response completion, provider close, replay completion, and command stop are not documented as one public lifecycle state machine. |
| `LIFECYCLE-02` | still open in `docs/architecture/contract-gap-audit.md` | `P4-API-03`, `P4-API-05` | remains open because stream completion provenance can be provider-authored or loop-synthesized without a public result distinction. |
| `DOC-01` | still open in `docs/architecture/contract-gap-audit.md` | `P4-GATE-01` | remains open because `go-llm-gateway/pkg/models` still reads as a gateway-owned model package while `docs/architecture/dependencies.md` says it is an alias facade over loop-owned contracts. |
| `DOC-02` | still open in `docs/architecture/contract-gap-audit.md` | `P4-GATE-01` | remains open because `agent-cli/internal/*` exported helpers remain application wiring, not downstream APIs, but that distinction is only partially documented. |
| `COMPAT-01` | still open risk note | `P4-API-03`, `P4-API-05`, `P4-GATE-01` | remains open as compatibility guidance for any future shared message/session contract change. |
| `COMPAT-02` | still open risk note | `P4-API-01`, `P4-API-03`, `P4-API-05`, `P4-GATE-01` | remains open as compatibility guidance for session stop behavior and persisted capture semantics. |
| `COMPAT-03` | still open risk note | `P4-API-02`, `P4-API-05`, `P4-GATE-01` | remains open as compatibility guidance for adding typed error classes while preserving legacy text during migration. |
| no Phase 2 audit row found | missing audit coverage | `P4-API-04`, `P4-API-06` | uncertain because the current audit does not map provider capability discovery or local unsupported-feature validation to explicit Phase 4 repair findings. |

### Reconciliation Findings

#### `P4-API-01` - Audit Reconciliation For Context Ownership

- `verdict`: `uncertain`
- `closure decision`: `remains open`
- `public evidence`: `CTX-02` is narrowed by
  `phase-2-session-runtime-ownership-repair` for session replay and recording
  relay cancellation, but `CTX-01` remains open for split session shape and
  call-time context ownership.
- `affected files / declarations`: `go-agent-loop/pkg/messages/session.go`;
  `go-llm-gateway/pkg/inference/session_inferencer.go`;
  `go-llm-gateway/pkg/testing/session_record.go`;
  `go-llm-gateway/pkg/testing/session_replay.go`;
  `agent-cli/internal/services/session.go`.
- `docs, examples, tests, audit, and API alignment`: partial alignment exists
  for replay and recorder relay cancellation, but the audit still records a
  wider context-contract gap and no validator-015 evidence is available for
  supersession.
- `reviewer commands`: `rg -n "CTX-01|CTX-02|context.Background|ConnectSession"`
  `docs go-llm-gateway go-agent-loop agent-cli`; `make typecheck`;
  `make test`.
- `exact repair work for non-pass rows`: add a cleanup/reconciliation idea that
  imports or supersedes validator-015, then update the audit to distinguish the
  closed replay/recorder relay subset from still-open public blocking-call and
  session request/config context ownership work.

#### `P4-API-02` - Audit Reconciliation For Typed Errors

- `verdict`: `fail`
- `closure decision`: `remains open`
- `public evidence`: `ERR-01` and `ERR-02` remain open in
  `docs/architecture/contract-gap-audit.md`; the audit states shared stream
  errors and session command failures still commonly expose wrapped text or
  `err.Error()` instead of a caller-actionable taxonomy.
- `affected files / declarations`: `go-agent-loop/pkg/messages.ErrorValue`;
  `go-agent-loop/pkg/participants/model_runner.go`;
  `go-agent-loop/pkg/participants/tool_runner.go`;
  provider stream adapters; `agent-cli/internal/services/session.go`.
- `docs, examples, tests, audit, and API alignment`: aligned on the existence
  of the gap, not on closure. No current public evidence or validator-015
  artifact proves typed error convergence.
- `reviewer commands`: `rg -n "ERR-01|ERR-02|NewErrorValue|err.Error|wrapSessionPhaseError"`
  `docs go-agent-loop go-llm-gateway agent-cli`; `make typecheck`;
  `make test`.
- `exact repair work for non-pass rows`: implement an additive typed error
  repair idea that gives shared stream, gateway/provider, replay, and CLI
  session failures stable error classes supporting `errors.Is` or `errors.As`,
  then update audit rows and deterministic tests to prove public classification
  without string parsing.

#### `P4-API-03` - Audit Reconciliation For Result And Lifecycle Contracts

- `verdict`: `fail`
- `closure decision`: `remains open`
- `public evidence`: `LIFECYCLE-01`, `LIFECYCLE-02`, `COMPAT-01`, and
  `COMPAT-02` remain open in the audit; they describe unresolved ambiguity in
  session completion, stream completion provenance, shared message/session
  contract changes, and persisted capture semantics.
- `affected files / declarations`: `go-agent-loop/pkg/messages`;
  `go-agent-loop/pkg/participants.ModelRunner`;
  `go-agent-loop/pkg/agentloop.AgenticLoop`;
  `go-llm-gateway/pkg/testing`; `agent-cli/internal/services/session.go`.
- `docs, examples, tests, audit, and API alignment`: current docs and audit
  agree that lifecycle/result hardening remains future work. No validator-015
  artifact proves the baseline has converged.
- `reviewer commands`: `rg -n "LIFECYCLE-01|LIFECYCLE-02|COMPAT-01|COMPAT-02|MESSAGE.END|SESSION.CLOSE|provider_closed"`
  `docs go-agent-loop go-llm-gateway agent-cli`; `make typecheck`;
  `make test`.
- `exact repair work for non-pass rows`: create an implementation idea for a
  documented public lifecycle/result state machine covering empty success,
  cancellation, partial success, provider-authored terminal events,
  loop-synthesized terminal events, replay completion, closed/drained state, and
  terminal failure, with replay and CLI fixture coverage.

#### `P4-API-04` - Audit Reconciliation For Provider Capability Discovery

- `verdict`: `uncertain`
- `closure decision`: `remains open`
- `public evidence`: `go-llm-gateway/README.md` includes a provider surface map,
  but the current audit has no explicit Phase 4 row covering tools, streaming,
  sessions, audio, image input, video output, reasoning, prompt caching, and
  provider-specific config with supported, unsupported, and unknown states.
- `affected files / declarations`: `go-llm-gateway/pkg/providers.Provider`;
  `go-llm-gateway/pkg/providers.SessionProvider`;
  `go-llm-gateway/pkg/gateway`; concrete provider packages.
- `docs, examples, tests, audit, and API alignment`: docs provide a broad
  provider package map, but audit-to-implementation evidence is missing for the
  full capability vocabulary required by `P4-API-04`.
- `reviewer commands`: `rg -n "Provider Surface Map|SessionProvider|InferStream|capabil|prompt cach|reasoning"`
  `go-llm-gateway docs`; `make typecheck`; `make test`.
- `exact repair work for non-pass rows`: add a provider capability discovery
  repair idea that defines the public capability vocabulary and deterministic
  tests for supported, unsupported, and unknown states before any checklist
  closure.

#### `P4-API-05` - Audit Reconciliation For Stream Semantics

- `verdict`: `fail`
- `closure decision`: `remains open`
- `public evidence`: stream-related audit rows `ERR-01`, `LIFECYCLE-01`,
  `LIFECYCLE-02`, `COMPAT-01`, `COMPAT-02`, and `COMPAT-03` remain open or
  risk-bearing. The audit says terminal stream and session semantics are not
  yet one public contract.
- `affected files / declarations`: `go-agent-loop/pkg/messages.StreamMessage`;
  `go-agent-loop/pkg/messages.ErrorValue`;
  `go-agent-loop/pkg/participants.ModelRunner`;
  `go-llm-gateway/pkg/inference`; provider stream implementations;
  `go-llm-gateway/pkg/testing`.
- `docs, examples, tests, audit, and API alignment`: audit and docs do not yet
  provide closure evidence that typed error details, cancellation, replay
  mismatch, partial output, and terminal events are preserved through a public
  stream surface.
- `reviewer commands`: `rg -n "ERR-01|LIFECYCLE-02|StreamMessage|MESSAGE.END|ERROR|replay mismatch|NewErrorValue"`
  `docs go-agent-loop go-llm-gateway agent-cli`; `make typecheck`;
  `make test`.
- `exact repair work for non-pass rows`: pair typed stream error repair with a
  public stream terminal-event contract and deterministic replay/provider
  adapter tests that distinguish cancellation, replay mismatch, partial output,
  provider terminal events, and synthesized terminal events.

#### `P4-API-06` - Audit Reconciliation For Local Unsupported-Feature Validation

- `verdict`: `uncertain`
- `closure decision`: `remains open`
- `public evidence`: no Phase 2 audit row maps unsupported stateless or session
  features to local validation before provider execution. Current docs mention
  provider differences, but they do not prove local validation errors with
  feature, provider, requested mode, and capability state.
- `affected files / declarations`: `go-llm-gateway/pkg/gateway`;
  `go-llm-gateway/pkg/providers/*`; `agent-cli/internal/agent`;
  `agent-cli/internal/services`.
- `docs, examples, tests, audit, and API alignment`: missing audit coverage and
  missing validator-015 evidence prevent closure.
- `reviewer commands`: `rg -n "unsupported|capabil|provider|requested mode|validation"`
  `docs go-llm-gateway agent-cli`; `make typecheck`; `make test`.
- `exact repair work for non-pass rows`: create a local unsupported-feature
  validation repair idea that fails unsupported stateless and session features
  before provider execution with inspectable typed errors and deterministic
  provider-fake tests.

#### `P4-API-07` - Audit Reconciliation For Dependency Ownership

- `verdict`: `uncertain`
- `closure decision`: `remains open`
- `public evidence`: `docs/architecture/dependencies.md` records Phase 2
  constructor and session runtime ownership repairs, and `CTX-02` is narrowed,
  but `CTX-01`, `DOC-01`, and `DOC-02` still show unresolved or only partially
  documented ownership boundaries.
- `affected files / declarations`: `go-agent-loop/pkg/agentloop.New`;
  `go-llm-gateway/pkg/providers/*`; `go-llm-gateway/pkg/models`;
  `agent-cli/internal/agent`; `agent-cli/internal/services`;
  `docs/architecture/dependencies.md`.
- `docs, examples, tests, audit, and API alignment`: aligned for the completed
  Phase 2 constructor/session runtime repairs, but not enough to close all
  filesystem, environment, process, transport, network, time, prompt, and
  provider runtime ownership surfaces required by `P4-API-07`.
- `reviewer commands`: `rg -n "DI-01|DI-02|CTX-01|DOC-01|DOC-02|constructor ownership|runtime ownership"`
  `docs go-agent-loop go-llm-gateway agent-cli`; `make typecheck`;
  `make test`.
- `exact repair work for non-pass rows`: add an audit cleanup idea that maps
  each dependency ownership surface to closed, open, or not-yet-reviewed status,
  then implement focused repairs only for surfaces still creating hidden IO,
  environment, process, network, transport, time, prompt, or provider defaults.

#### `P4-GATE-01` - Audit And Validator-015 Gate Reconciliation

- `verdict`: `fail`
- `closure decision`: `remains open`
- `public evidence`: validator-015 is missing from committed evidence, provider
  capability and unsupported-feature rows lack audit mapping, and multiple
  audit rows remain open or only partially narrowed.
- `affected files / declarations`: `docs/architecture/contract-gap-audit.md`;
  `docs/architecture/dependencies.md`; `docs/internal/checklist.md`;
  `docs/internal/phase-4-api-contract-repair-validator.md`;
  public API, docs, examples, and tests cited by `P4-API-01` through
  `P4-API-07`.
- `docs, examples, tests, audit, and API alignment`: not aligned. The current
  public docs and audit identify some repaired Phase 2 ownership subsets, but
  the full Phase 4 public API contract hardening baseline does not yet have
  one reconciled, reviewer-verifiable evidence set.
- `reviewer commands`: `rg -n "validator 015|validator-015|P4-API|P4-GATE|ERR-|CTX-|LIFECYCLE-|COMPAT-|DOC-"`
  `prd.md docs go-agent-loop go-llm-gateway agent-cli`; `make typecheck`;
  `make test`.
- `exact repair work for non-pass rows`: run a cleanup/reconciliation lane that
  either restores validator-015 or records its explicit supersession, maps every
  open audit row to `P4-API-01` through `P4-API-07` and `P4-GATE-01`, and adds
  missing audit rows for provider capability discovery and local unsupported
  feature validation.

## Typed Errors And Stream Contract Validation

### Evidence Inputs

- `go-agent-loop/pkg/messages/agent_messages.go`
- `go-agent-loop/pkg/participants/model_runner.go`
- `go-llm-gateway/pkg/gateway/gateway.go`
- `go-llm-gateway/pkg/gateway/interaction_gateway.go`
- `go-llm-gateway/pkg/gateway/interaction_types.go`
- `go-llm-gateway/pkg/testing/session_websocket_dialer.go`
- `go-llm-gateway/README.md`
- `go-agent-loop/README.md`
- `agent-cli/docs/session-record-replay.md`
- `docs/architecture/contract-gap-audit.md`
- deterministic tests under `go-llm-gateway/pkg/gateway`,
  `go-llm-gateway/pkg/testing`, `go-llm-gateway/pkg/providers/*`,
  `go-agent-loop/pkg/participants`, and `agent-cli/test/integration`

### `P4-API-02` - Typed Gateway, Provider, Replay, And CLI Errors

- `verdict`: `fail`
- `closure decision`: `remains open`
- `public evidence`: `go-agent-loop/pkg/messages.ErrorValue` is exported and
  has structured fields (`ErrorType`, `Code`, `Param`, and `EventID`), and
  `go-llm-gateway/pkg/gateway.InteractionError` exposes `Code` and
  `Retryable`. Those fields are only partial typed-error evidence. The shared
  streaming path still commonly emits `messages.NewErrorValue(err.Error())`;
  `go-agent-loop/pkg/participants.ModelRunner.emitSyntheticDeltas` collapses
  non-streaming failures into a string-only `ERROR`; and
  `go-llm-gateway/pkg/testing.ReplayWebSocketDialer.Err()` returns ordinary
  `fmt.Errorf(...)` replay divergence or incompletion errors with no exported
  type, sentinel, or `errors.Is` / `errors.As` contract. Deterministic tests
  prove some typed errors exist for interaction fixture validation through
  `errors.As(err, &InteractionFixtureValidationError)`, and prove cancellation
  preservation in selected loop/session paths through `errors.Is(err,
  context.Canceled)`, but they do not prove one public taxonomy across gateway,
  provider, CLI, replay, validation, and stream boundaries.
- `affected files / declarations`: `go-agent-loop/pkg/messages.ErrorValue`;
  `go-agent-loop/pkg/messages.NewErrorValue`;
  `go-agent-loop/pkg/messages.NewErrorValueWithDetails`;
  `go-agent-loop/pkg/participants.ModelRunner.emitSyntheticDeltas`;
  `go-llm-gateway/pkg/gateway.InteractionError`;
  `go-llm-gateway/pkg/gateway.InteractionFixtureValidationError`;
  `go-llm-gateway/pkg/testing.ReplayWebSocketDialer.Err`;
  `go-llm-gateway/pkg/testing.replayWebSocketConn.setErrLocked`;
  `agent-cli/internal/services/session_runtime.go`;
  `agent-cli/internal/services/session.go`.
- `docs, examples, tests, audit, and API alignment`: not aligned. Public docs
  document replay divergence by matching error text in
  `agent-cli/docs/session-record-replay.md`, while the architecture audit
  leaves `ERR-01`, `ERR-02`, and `COMPAT-03` open because stream and session
  command failures still depend on free-form strings. Tests currently assert
  some human-readable substrings such as "replay divergence" instead of a typed
  replay error contract.
- `reviewer commands`: `rg -n "type ErrorValue|NewErrorValue|InteractionError|InteractionFixtureValidationError|ReplayWebSocketDialer|replay divergence|errors\\.As|errors\\.Is"`
  `go-agent-loop/pkg go-llm-gateway/pkg agent-cli`; `rg -n
  "ERR-01|ERR-02|COMPAT-03|Replay Divergence Errors|typed error|errors\\.As|errors\\.Is"`
  `docs go-agent-loop/README.md go-llm-gateway/README.md agent-cli/docs`;
  `make typecheck`; `make test`.
- `exact repair work for non-pass rows`: implement an additive typed-error
  repair idea that introduces stable caller-actionable error kinds for
  validation, unsupported feature, provider request/transport, provider stream,
  replay divergence, replay incomplete, cancellation, timeout, session close,
  and capture persistence. Preserve legacy messages during migration, but add
  exported error types or sentinels that support `errors.Is` or `errors.As` and
  deterministic tests that prove classification without substring matching.

### `P4-API-05` - Stream Terminal Events And Error Preservation

- `verdict`: `fail`
- `closure decision`: `remains open`
- `public evidence`: public stream types include `MESSAGE.END`, `ERROR`,
  `SESSION.CLOSE`, `RESPONSE.CANCEL`, and content start/delta/end events.
  `go-agent-loop/pkg/participants.ModelRunner.drainStream` forwards provider
  stream events unchanged until `MESSAGE.END` or `ERROR`, but if the provider
  channel closes without `MESSAGE.END`, it emits a synthetic `MESSAGE.END`
  without an observable provenance field. The non-streaming fallback path also
  synthesizes `MESSAGE.START` through `MESSAGE.END`, and on error emits a
  string-only `ERROR`. Session replay can preserve pre-cancellation partial
  output in tests, and record/replay relay cancellation preserves
  `context.Canceled` in selected CLI tests, but replay mismatch is returned as
  an untyped Go error rather than an observable stream event with structured
  error details.
- `affected files / declarations`: `go-agent-loop/pkg/messages.StreamMessage`;
  `go-agent-loop/pkg/messages.StreamMessageType`;
  `go-agent-loop/pkg/messages.MessageEndValue`;
  `go-agent-loop/pkg/messages.ErrorValue`;
  `go-agent-loop/pkg/participants.ModelRunner.drainStream`;
  `go-agent-loop/pkg/participants.ModelRunner.emitSyntheticDeltas`;
  `go-llm-gateway/pkg/gateway.DefaultGateway.InferStream`;
  provider `InferStream` implementations; `go-llm-gateway/pkg/testing`
  replay helpers; `agent-cli/internal/services/session.go`.
- `docs, examples, tests, audit, and API alignment`: not aligned. Public docs
  show ordinary stream event shapes and session replay divergence text, but do
  not define when terminal events are provider-authored versus loop-synthesized
  or how cancellation, replay mismatch, partial output, and terminal failure
  are preserved through one public stream surface. The audit keeps
  `LIFECYCLE-02`, `ERR-01`, `ERR-02`, and compatibility rows open for the same
  reason.
- `reviewer commands`: `rg -n "StreamTypeMessageEnd|StreamTypeError|StreamTypeSessionClose|NewErrorValue|drainStream|emitSyntheticDeltas|InferStream|replay divergence|context\\.Canceled"`
  `go-agent-loop/pkg go-llm-gateway/pkg agent-cli`; `rg -n
  "LIFECYCLE-02|ERR-01|ERR-02|Replay Divergence Errors|MESSAGE\\.END|SESSION\\.CLOSE|cancellation semantics"`
  `docs go-agent-loop/README.md go-llm-gateway/README.md agent-cli/docs`;
  `make typecheck`; `make test`.
- `exact repair work for non-pass rows`: define the public stream terminal
  contract for provider-authored end, loop-synthesized end, cancellation, replay
  mismatch, partial output, session close, and terminal failure. Add structured
  error payload preservation for stream `ERROR` events, decide which failures
  remain in-band versus returned Go errors, and add deterministic fake-provider,
  replay, cancellation, and CLI tests proving each terminal path without live
  credentials.

### `P4-API-03` - Result Surface Impact From Typed Error And Stream Semantics

- `verdict`: `fail`
- `closure decision`: `remains open`
- `public evidence`: story 003 evidence directly affects result contracts
  because stream completion and terminal failure currently collapse several
  outcomes into either a synthetic `MESSAGE.END`, a string-only `ERROR`, or a
  returned untyped replay/CLI error. The current public result and stream
  surfaces do not let callers distinguish empty success, partial success before
  cancellation, provider-authored completion, loop-synthesized completion, replay
  mismatch, and terminal failure from one documented contract.
- `affected files / declarations`: `go-agent-loop/pkg/messages.InferenceResult`;
  `go-agent-loop/pkg/messages.StreamMessage`;
  `go-agent-loop/pkg/participants.ModelRunner`;
  `go-llm-gateway/pkg/gateway.InferenceResponse`;
  `go-llm-gateway/pkg/testing.ReplayWebSocketDialer.Err`;
  `agent-cli/internal/services/session.go`.
- `docs, examples, tests, audit, and API alignment`: not aligned. Existing
  tests prove selected behaviors, but docs and exported contracts do not expose
  the result-state distinctions required by `P4-API-03`; `LIFECYCLE-01`,
  `LIFECYCLE-02`, `COMPAT-01`, and `COMPAT-02` remain open in the audit.
- `reviewer commands`: `rg -n "InferenceResult|InferenceResponse|MessageEndValue|ErrorValue|provider_closed|replay incomplete|replay divergence"`
  `go-agent-loop/pkg go-llm-gateway/pkg agent-cli`; `rg -n
  "P4-API-03|LIFECYCLE-01|LIFECYCLE-02|COMPAT-01|COMPAT-02"`
  `docs/internal/checklist.md docs/architecture/contract-gap-audit.md`;
  `make typecheck`; `make test`.
- `exact repair work for non-pass rows`: include result-state work in the typed
  stream repair: define explicit public outcome states for empty success,
  partial success, cancellation, provider terminal failure, replay divergence,
  replay incomplete, closed/drained state, and synthesized completion, then
  update docs and deterministic tests to prove those states at the API boundary.

## Story 003 Closure

The typed errors and stream contract convergence story passes for validator
purposes: the report verifies current public typed-error and stream evidence,
marks `P4-API-02`, `P4-API-03`, and `P4-API-05` as failed rows that must remain
open, cites affected public declarations and deterministic commands, and scopes
future repair work without implementing the repair inside this validator lane.

## Story 002 Closure

The audit and validator-015 reconciliation story passes for validator purposes:
the report compares the available audit rows, marks the missing validator-015
artifact as uncertain, maps stale or unresolved audit evidence to Phase 4 rows,
and names exact future repair or cleanup work for every non-pass row. No
checklist row may close from story 002 alone.

## Current Story Status

Stories 001, 002, and 003 are complete. The report now establishes the Phase 4
validator subject, checklist row coverage, evidence rules, required finding
shape, audit/validator-015 reconciliation findings, and typed-error/stream
contract findings. Capability, validation, dependency, result, context,
lifecycle, and final planner decision findings remain deferred to later
validator stories so each pass can compare the current public API, docs,
examples where present, audit rows, tests, and deterministic command evidence at
the correct depth.
