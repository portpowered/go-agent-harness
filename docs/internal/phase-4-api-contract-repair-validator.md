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

## Provider Capabilities And Local Validation Contract Validation

### Evidence Inputs

- `go-llm-gateway/pkg/providers/provider.go`
- `go-llm-gateway/pkg/providers/session_provider.go`
- `go-llm-gateway/pkg/gateway/gateway.go`
- `go-llm-gateway/pkg/gateway/session_gateway.go`
- `go-llm-gateway/pkg/gateway/interaction_types.go`
- `go-llm-gateway/pkg/gateway/interaction_gateway.go`
- `go-llm-gateway/pkg/models/session.go`
- concrete provider implementations under `go-llm-gateway/pkg/providers/*`
- `go-llm-gateway/README.md`
- `agent-cli/docs/session-record-replay.md`
- `docs/architecture/contract-gap-audit.md`
- deterministic tests under `go-llm-gateway/pkg/gateway`,
  `go-llm-gateway/pkg/providers/*`, and `agent-cli/test/integration`

### `P4-API-04` - Provider Capability Discovery

- `verdict`: `fail`
- `closure decision`: `remains open`
- `public evidence`: `go-llm-gateway/README.md` has a provider surface map for
  stateless `Infer`, stateless `InferStream`, and session `ConnectSession`.
  That table is useful evidence, but it is narrower than the checklist row.
  The exported `providers.Provider` and `providers.SessionProvider`
  interfaces expose behavior methods and `Name()`, not a capability discovery
  method or data structure. Public request types accept tools, text, image,
  audio, video, reasoning (`ThinkingConfig`), prompt cache control
  (`CacheControlConfig`), raw provider `Config`, and session modalities/audio
  settings, but there is no exported supported/unsupported/unknown vocabulary
  that lets callers ask whether a configured provider supports each requested
  feature before issuing a request.
- `affected files / declarations`: `go-llm-gateway/pkg/providers.Provider`;
  `go-llm-gateway/pkg/providers.SessionProvider`;
  `go-llm-gateway/pkg/providers.InferenceRequest`;
  `go-llm-gateway/pkg/providers.ThinkingConfig`;
  `go-llm-gateway/pkg/providers.CacheControlConfig`;
  `go-llm-gateway/pkg/gateway.InferenceRequest`;
  `go-llm-gateway/pkg/gateway.InteractionRequest`;
  `go-llm-gateway/pkg/gateway.InteractionContentType`;
  `go-llm-gateway/pkg/models.SessionConfig`;
  concrete provider packages.
- `docs, examples, tests, audit, and API alignment`: not aligned. Docs disclose
  a coarse provider surface and examples show how to call a provider, but docs,
  exported APIs, audit rows, and deterministic tests do not define one public
  capability matrix for tools, streaming, sessions, audio, image input, video
  output, reasoning, prompt caching, and provider-specific config. Current
  provider comments also describe some provider-specific fields as ignored by
  other providers, which is behavior documentation rather than a discoverable
  capability contract.
- `reviewer commands`: `rg -n "type Provider interface|type
  SessionProvider interface|ThinkingConfig|CacheControlConfig|InteractionContent(Image|Audio|Video)|SessionConfig|Provider Surface Map"`
  `go-llm-gateway`; `rg -n "capabil|unsupported|unknown|prompt cach|reasoning"`
  `docs go-llm-gateway agent-cli`; `make typecheck`; `make test`.
- `exact repair work for non-pass rows`: introduce a public provider
  capability contract with an explicit tri-state (`supported`, `unsupported`,
  `unknown`) for tools, stateless streaming, sessions, audio input/audio
  output, image input, video output, reasoning, prompt caching, and raw
  provider config. Expose it through the provider/gateway surface without
  importing concrete provider packages, document the matrix, and add
  deterministic fake-provider and concrete-provider tests proving the published
  capability values.

### `P4-API-06` - Local Unsupported-Feature Validation

- `verdict`: `fail`
- `closure decision`: `remains open`
- `public evidence`: some local validation exists, but it is not the
  unsupported-feature validation required by the checklist. `DefaultGateway`
  forwards `InferenceRequest` directly to the provider, and
  `DefaultSessionGateway.ConnectSession` forwards `SessionConfig` directly to
  the session provider. `DefaultGateway.Interact` validates tool-result
  consistency before provider execution and emits `tool_result_validation_error`
  without calling the provider, which is good local validation evidence for one
  interaction-continuation rule. It does not validate unsupported tools,
  streaming, sessions, audio, image, video, reasoning, prompt caching, or
  provider-specific config against provider capabilities. Concrete providers
  still handle unsupported or model-specific cases independently; for example
  the fal provider returns a formatted unsupported-model error, and its
  `InferStream` returns an immediately closed channel for sync-only media flows
  instead of an inspectable unsupported streaming error.
- `affected files / declarations`: `go-llm-gateway/pkg/gateway.DefaultGateway`;
  `go-llm-gateway/pkg/gateway.DefaultGateway.Infer`;
  `go-llm-gateway/pkg/gateway.DefaultGateway.InferStream`;
  `go-llm-gateway/pkg/gateway.DefaultGateway.Interact`;
  `go-llm-gateway/pkg/gateway.DefaultSessionGateway.ConnectSession`;
  `go-llm-gateway/pkg/gateway.InteractionError`;
  `go-llm-gateway/pkg/providers.Provider`;
  `go-llm-gateway/pkg/providers.SessionProvider`;
  concrete provider packages including `pkg/providers/fal`,
  `pkg/providers/anthropic`, `pkg/providers/gemini`, `pkg/providers/openai`,
  and `pkg/providers/grok`.
- `docs, examples, tests, audit, and API alignment`: not aligned. Tests prove
  local tool-result validation prevents provider calls, but no deterministic
  public test proves unsupported stateless or session request features fail
  locally with an inspectable error identifying feature, provider, requested
  mode, and capability state. Docs describe provider differences but do not
  promise a local unsupported-feature error surface. The audit also lacks an
  explicit provider capability or unsupported-feature row.
- `reviewer commands`: `rg -n "validateInteractionToolResults|provider.calls
  != 0|tool_result_validation_error|InferStream|unsupported model|ConnectSession"`
  `go-llm-gateway/pkg agent-cli`; `rg -n "unsupported|capabil|provider
  differences|Provider Surface Map"` `docs go-llm-gateway agent-cli`; `make
  typecheck`; `make test`.
- `exact repair work for non-pass rows`: add gateway-level unsupported-feature
  validation that runs before stateless and session provider execution. The
  error must be typed and inspectable, identify the feature, provider,
  requested mode, and capability state, preserve cancellation semantics, and
  include deterministic tests proving providers are not called for unsupported
  tools, streaming, sessions, audio, image, video, reasoning, prompt caching,
  and provider-specific config requests.

### `P4-GATE-01` - Capability And Validation Gate Impact

- `verdict`: `fail`
- `closure decision`: `remains open`
- `public evidence`: the current public docs, exported provider/gateway APIs,
  audit rows, and deterministic tests do not describe the same capability and
  unsupported-feature validation contract. The README's coarse provider surface
  table is public evidence of provider differences, but no public API exposes
  those differences as caller-queryable supported, unsupported, or unknown
  states, and local validation is proven only for interaction tool-result
  consistency.
- `affected files / declarations`: all declarations cited by `P4-API-04` and
  `P4-API-06`; `go-llm-gateway/README.md`;
  `docs/architecture/contract-gap-audit.md`;
  `docs/internal/checklist.md`.
- `docs, examples, tests, audit, and API alignment`: not aligned for this
  capability/validation slice. The row cannot close from CI success or README
  prose until docs, public APIs, audit rows, examples where present, and
  deterministic tests converge on the same contract.
- `reviewer commands`: `rg -n "Provider Surface Map|type Provider
  interface|type SessionProvider interface|validateInteractionToolResults|tool_result_validation_error|unsupported model"`
  `go-llm-gateway docs agent-cli`; `make typecheck`; `make test`.
- `exact repair work for non-pass rows`: complete the capability discovery and
  local unsupported-feature validation repair as one implementation lane, then
  update docs and audit rows so `P4-API-04`, `P4-API-06`, and the relevant
  `P4-GATE-01` slice cite the same public contract and deterministic proof.

## Story 004 Closure

The provider capability and local request validation convergence story passes
for validator purposes: the report verifies the current public API, docs, audit
coverage, and deterministic tests for `P4-API-04` and `P4-API-06`; marks both
rows and their `P4-GATE-01` impact as failed rows that must remain open; cites
affected public declarations and reviewer commands; and scopes exact future
repair work without implementing the repair inside this validator lane.

## Dependency, Result, Context, And Lifecycle Contract Validation

### Evidence Inputs

- `go-agent-loop/pkg/messages/session.go`
- `go-agent-loop/pkg/messages/buffers.go`
- `go-agent-loop/pkg/messages/agent_messages.go`
- `go-agent-loop/pkg/participants/model_runner.go`
- `go-llm-gateway/pkg/gateway/gateway.go`
- `go-llm-gateway/pkg/gateway/session_gateway.go`
- `go-llm-gateway/pkg/inference/session_inferencer.go`
- `go-llm-gateway/pkg/models/session.go`
- `go-llm-gateway/pkg/testing/session_record.go`
- `go-llm-gateway/pkg/testing/session_replay.go`
- `agent-cli/internal/agent/provider_runtime.go`
- `agent-cli/internal/services/session.go`
- `agent-cli/internal/services/session_runtime.go`
- `agent-cli/internal/agent/executor.go`
- `docs/architecture/contract-gap-audit.md`
- `docs/architecture/dependencies.md`
- deterministic tests under `go-agent-loop/pkg/participants`,
  `go-agent-loop/test/functional`, `go-llm-gateway/pkg/testing`,
  `go-llm-gateway/pkg/inference`, and `agent-cli/test/integration`

### `P4-API-01` - Blocking Context, Cancellation, And Timeout Ownership

- `verdict`: `uncertain`
- `closure decision`: `remains open`
- `public evidence`: public gateway and session entrypoints accept caller-owned
  `context.Context`: `DefaultGateway.Infer`, `DefaultGateway.InferStream`,
  `DefaultSessionGateway.ConnectSession`, `messages.SessionInferencer`, and
  `messages.Session.Send`. Session replay and recording helpers now accept
  explicit relay lifecycle contexts, and deterministic tests prove selected
  cancellation paths preserve `context.Canceled`. That is partial positive
  evidence. The row still cannot close because `TypedBuffer.Write` returns only
  `false` for both cancellation and full-buffer drop, `Session.Send` has the
  same ambiguous bool surface, `SessionGatewayInferencer` still bakes session
  shape into constructor options while `ConnectSession(ctx)` carries only
  lifetime, and the audit keeps `CTX-01`, `LIFECYCLE-01`, and `COMPAT-02` open
  for split session context and command-stop behavior.
- `affected files / declarations`: `go-agent-loop/pkg/messages.SessionInferencer`;
  `go-agent-loop/pkg/messages.Session`; `go-agent-loop/pkg/messages.TypedBuffer`;
  `go-agent-loop/pkg/participants.ModelRunner.runSession`;
  `go-llm-gateway/pkg/gateway.DefaultGateway.Infer`;
  `go-llm-gateway/pkg/gateway.DefaultGateway.InferStream`;
  `go-llm-gateway/pkg/gateway.DefaultSessionGateway.ConnectSession`;
  `go-llm-gateway/pkg/inference.SessionGatewayInferencer.ConnectSession`;
  `go-llm-gateway/pkg/testing.SessionRecorder`;
  `go-llm-gateway/pkg/testing.SessionReplayer`;
  `agent-cli/internal/services.RunSession`.
- `docs, examples, tests, audit, and API alignment`: partially aligned for
  replay/record relay cancellation after Phase 2, but not aligned for the full
  public blocking-call contract. Docs and audit still require one explicit
  session request/config and lifecycle contract before cancellation, timeout,
  and stop semantics can close.
- `reviewer commands`: `rg -n "ConnectSession\\(ctx|WithReplayContext|WithSessionRelayContext|TypedBuffer|Write\\(ctx|context\\.Canceled|CTX-01|LIFECYCLE-01|COMPAT-02"`
  `go-agent-loop go-llm-gateway agent-cli docs`; `make typecheck`;
  `make test`.
- `exact repair work for non-pass rows`: define a public session request and
  lifecycle contract that separates cancellation/deadline ownership from
  session shape, add explicit write/send outcomes for cancellation versus full
  buffers or closed sessions, and add deterministic tests for gateway, session,
  replay, record, and CLI timeout/cancellation paths without live credentials.

### `P4-API-03` - Result, Buffer, Session, And Stream Outcome States

- `verdict`: `fail`
- `closure decision`: `remains open`
- `public evidence`: public stream messages expose `MESSAGE.END`, `ERROR`,
  `SESSION.CLOSE`, and `RESPONSE.CANCEL`, and `InteractionEvent` includes a
  `Completed` field for interaction output. Those surfaces do not form one
  explicit outcome contract. `ModelRunner.drainStream` emits a synthetic
  `MESSAGE.END` when a provider channel closes without an end event;
  `emitSyntheticDeltas` maps non-streaming success into the same end event and
  non-streaming failure into a string-only `ERROR`; `TypedBuffer.Read` returns
  `(zero, false)` for empty buffers only; and `SessionReplayer.Err` returns
  untyped replay divergence or incomplete-close errors out of band. Callers
  cannot distinguish empty success, partial success before cancellation,
  provider-authored completion, loop-synthesized completion, replay mismatch,
  replay incomplete, closed/drained state, and terminal failure through one
  documented public API.
- `affected files / declarations`: `go-agent-loop/pkg/messages.InferenceResult`;
  `go-agent-loop/pkg/messages.StreamMessage`;
  `go-agent-loop/pkg/messages.TypedBuffer`;
  `go-agent-loop/pkg/messages.Session`;
  `go-agent-loop/pkg/participants.ModelRunner.drainStream`;
  `go-agent-loop/pkg/participants.ModelRunner.emitSyntheticDeltas`;
  `go-llm-gateway/pkg/gateway.InferenceResponse`;
  `go-llm-gateway/pkg/gateway.InteractionEvent`;
  `go-llm-gateway/pkg/testing.SessionReplayer.Err`;
  `agent-cli/internal/services.sessionOutput`.
- `docs, examples, tests, audit, and API alignment`: not aligned. Tests prove
  several concrete behaviors, but docs, audit rows `LIFECYCLE-01`,
  `LIFECYCLE-02`, `COMPAT-01`, and `COMPAT-02`, and exported declarations do
  not describe the same public result-state vocabulary.
- `reviewer commands`: `rg -n "type TypedBuffer|func \\(b \\*TypedBuffer|StreamTypeMessageEnd|StreamTypeError|StreamTypeSessionClose|Completed|provider_closed|session replay closed before|replay divergence"`
  `go-agent-loop go-llm-gateway agent-cli docs`; `make typecheck`;
  `make test`.
- `exact repair work for non-pass rows`: add an additive result/outcome
  vocabulary covering empty success, cancellation, partial success,
  closed/drained, provider-authored end, loop-synthesized end, replay
  divergence, replay incomplete, and terminal failure. Wire the vocabulary into
  buffer/session/stream result surfaces or document where a legacy bool/error
  remains compatibility-staged, then add deterministic fake-provider, replay,
  and CLI tests.

### `P4-API-07` - Dependency Ownership And Side Effects

- `verdict`: `fail`
- `closure decision`: `remains open`
- `public evidence`: Phase 2 repaired important constructor ownership subsets:
  tool execution ownership is explicit at `agentloop.New`, stateless provider
  HTTP runtime policy is composed in `agent-cli/internal/agent`, and scoped
  Grok/OpenAI session runtime wiring is centralized in
  `agent-cli/internal/services/session_runtime.go`. The full Phase 4 row is
  broader. The audit still records `DI-03` because
  `Executor.loadSystemPrompt` mixes prompt assembly with filesystem reads,
  `workspace.EnsureAgentsMD`, config loading, and skills metadata loading.
  `SessionGatewayInferencer` exposes only model, voice, and instructions even
  though `models.SessionConfig` has modalities, audio formats, tools, turn
  detection, and provider-specific config. Session capture and fixture helpers
  own filesystem and time side effects directly through `os.ReadFile`,
  `os.WriteFile`, and `time.Now().UTC()`. `agent-cli` application wiring also
  legitimately owns local filesystem, environment, process, terminal, storage,
  network, and provider selection concerns, but the public docs do not yet map
  every such surface to caller-owned, injected, side-effect free, or explicitly
  open work.
- `affected files / declarations`: `go-agent-loop/pkg/agentloop.New`;
  `go-llm-gateway/pkg/inference.SessionGatewayInferencer`;
  `go-llm-gateway/pkg/models.SessionConfig`;
  `go-llm-gateway/pkg/testing.NewSessionRecorder`;
  `go-llm-gateway/pkg/testing.NewSessionReplayer`;
  `go-llm-gateway/pkg/testing.SessionRecorder.FlushToFile`;
  `agent-cli/internal/agent.Executor.loadSystemPrompt`;
  `agent-cli/internal/agent.buildProviderHTTPRuntime`;
  `agent-cli/internal/services.sessionRuntimePlan`;
  `docs/architecture/dependencies.md`;
  `docs/architecture/contract-gap-audit.md`.
- `docs, examples, tests, audit, and API alignment`: not aligned. The docs and
  audit accurately describe several repaired Phase 2 ownership seams, but they
  also name still-open prompt resolution, session request/config, alias facade,
  and internal wiring documentation gaps. There is no complete reviewer-facing
  matrix for filesystem, environment, process, transport, network, time,
  prompt, provider runtime, constructor, and session dependencies.
- `reviewer commands`: `rg -n "DI-03|CTX-01|DOC-01|DOC-02|loadSystemPrompt|EnsureAgentsMD|SessionGatewayInferencer|SessionConfig|time\\.Now|os\\.ReadFile|os\\.WriteFile|buildProviderHTTPRuntime|sessionRuntimePlan"`
  `docs go-agent-loop go-llm-gateway agent-cli`; `make typecheck`;
  `make test`.
- `exact repair work for non-pass rows`: create an implementation lane that
  splits prompt assembly into pure composition plus injected filesystem/config/
  system-info/skills loaders, expands or supersedes the loop-facing session
  request/config adapter, documents the `pkg/models` alias role and
  `agent-cli/internal/*` wiring boundary, and records every remaining
  filesystem, environment, process, transport, network, and time dependency as
  caller-owned, injected, side-effect free, or explicitly open work.

### `P4-GATE-01` - Dependency, Result, Context, And Lifecycle Gate Impact

- `verdict`: `fail`
- `closure decision`: `remains open`
- `public evidence`: docs, tests, public APIs, and audit rows align on some
  repaired ownership subsets, especially constructor tool execution,
  stateless provider HTTP runtime injection, and session relay cancellation.
  They do not align on the broader Phase 4 contract because `P4-API-01`,
  `P4-API-03`, and `P4-API-07` remain non-pass in this story, while earlier
  validator sections also keep typed errors, stream semantics, provider
  capabilities, and local unsupported-feature validation open.
- `affected files / declarations`: every declaration cited by `P4-API-01`,
  `P4-API-03`, and `P4-API-07`; `docs/architecture/contract-gap-audit.md`;
  `docs/architecture/dependencies.md`;
  `docs/internal/checklist.md`;
  `docs/internal/phase-4-api-contract-repair-validator.md`.
- `docs, examples, tests, audit, and API alignment`: not aligned for this
  slice. The validator can document non-convergence, but the checklist row
  itself cannot close until public APIs, docs, examples where present, audit
  rows, deterministic tests, and reviewer commands describe the same current
  dependency/result/context/lifecycle contract.
- `reviewer commands`: `rg -n "P4-API-01|P4-API-03|P4-API-07|P4-GATE-01|CTX-01|DI-03|LIFECYCLE-|COMPAT-|DOC-"`
  `docs go-agent-loop go-llm-gateway agent-cli`; `make typecheck`;
  `make test`.
- `exact repair work for non-pass rows`: complete the dependency ownership,
  session request/context, result-state, and lifecycle repair lane before
  marking the Phase 4 hardening gate complete. Preserve the already repaired
  Phase 2 ownership seams, but do not proceed to a new Phase 4 feature batch
  from CI success alone.

## Story 005 Closure

The dependency, result, context, and lifecycle convergence story passes for
validator purposes: the report verifies caller-owned cancellation evidence,
public result/buffer/session/stream outcome surfaces, constructor and runtime
ownership seams, prompt/filesystem/time side effects, docs, audit rows, and
deterministic commands. It marks `P4-API-01` as uncertain and `P4-API-03`,
`P4-API-07`, and the relevant `P4-GATE-01` slice as failed rows that must
remain open, with exact future repair work scoped outside this validator lane.

## Story 002 Closure

The audit and validator-015 reconciliation story passes for validator purposes:
the report compares the available audit rows, marks the missing validator-015
artifact as uncertain, maps stale or unresolved audit evidence to Phase 4 rows,
and names exact future repair or cleanup work for every non-pass row. No
checklist row may close from story 002 alone.

## Final Convergence Decision

The Phase 4 public API contract hardening repair baseline is not ready to close.
The validator stories are complete, but every checklist row under review remains
open because the current public APIs, public docs, examples where present, audit
rows, deterministic tests, and reviewer-runnable commands do not yet describe
one converged current contract.

This final section is authoritative for the reviewed head after mergeability
reconciliation with `origin/main`. Earlier story sections preserve the evidence
gathered before that merge; where incoming Phase 4 starter repairs changed the
runtime evidence, the final row table below supersedes those earlier row
summaries.

CI or local quality success is supporting evidence only. It is not row closure
evidence for this baseline because the missing or conflicting contracts are
observable public API, documentation, audit, and deterministic proof gaps.

| Checklist row | Final verdict | Closure decision | Public evidence summary | Reviewer commands | Future repair work |
| --- | --- | --- | --- | --- | --- |
| `P4-API-01` | `uncertain` | remains open | Public context-first cancellation evidence exists on major loop, gateway, session, replay, and buffer surfaces, including `TypedBuffer.ReadContext` and repaired replay relay cancellation. The row still lacks reconciled evidence across every blocking/provider entrypoint, timeout behavior, docs, examples, and remaining ambiguous bool helpers such as `Session.Send`. | `rg -n "ConnectSession\\(ctx|ReadContext|WithReplayContext|WithSessionRelayContext|Session.Send|context\\.Canceled|P4-CTX" go-agent-loop go-llm-gateway agent-cli docs`; `make typecheck`; `make test` | reconcile the audit row list, close only covered declarations, and continue `tasks/ideas-to-review/go-llm-gateway/phase-4-dependency-result-context-lifecycle-contract.md` for remaining session/result helpers |
| `P4-API-02` | `uncertain` | remains open | Public gateway/provider error classes, typed wrappers, `messages.ErrorValue.Classification`, README guidance, replay mismatch classification, and representative `errors.Is` / `errors.As` tests now exist. The row remains open because classification is additive and not yet proven uniformly across all provider, validation, direct stream, session, CLI, and replay surfaces. | `rg -n "GatewayError|ErrReplayMismatch|ErrorClassification|NewStreamErrorValue|Classification|errors\\.Is|errors\\.As" go-agent-loop/pkg go-llm-gateway/pkg agent-cli docs`; `make typecheck`; `make test` | reconcile stale audit findings with the implemented taxonomy, finish classification on remaining provider/session/validation paths, and narrow `tasks/ideas-to-review/go-llm-gateway/phase-4-typed-errors-and-stream-terminal-contract.md` to unproven surfaces |
| `P4-API-03` | `uncertain` | remains open | `ExecuteResult.FinalText`, `Stream.Outcome`, `TypedBuffer.ReadContext`, interaction output states, and replay mismatch tests provide additive result-state evidence. The row still cannot close because row-level evidence does not cover all public result values and stream events for success, empty success, partial success, terminal failure, replay divergence, cancellation, provider rejection, closed/drained state, and synthesized completion. | `rg -n "FinalText|StreamOutcome|Outcome\\(|ReadContext|OutputState|partial|replay divergence|P4-RESULT|P4-STREAM" go-agent-loop go-llm-gateway agent-cli docs`; `make typecheck`; `make test` | add explicit row mapping for every public result and stream-event declaration, then continue `tasks/ideas-to-review/go-llm-gateway/phase-4-dependency-result-context-lifecycle-contract.md` for remaining ambiguous helpers |
| `P4-API-04` | `uncertain` | remains open | Consumers can now query a public capability contract with supported, unsupported, and unknown states through `go-llm-gateway/pkg/capabilities`, `providers.CapabilityReporter`, `gateway.CapabilityReporter`, `DefaultGateway.Capabilities`, and `DefaultSessionGateway.Capabilities`. Closure still needs concrete provider coverage and stale audit reconciliation across tools, streaming, sessions, audio, image input, video output, reasoning, prompt caching, provider config, and provider-specific limits. | `rg -n "capabil|Capability|Capabilities" docs/architecture/contract-gap-audit.md go-llm-gateway/pkg go-llm-gateway/README.md`; `make typecheck`; `make test` | reconcile `P4-CAP-01` with the implemented API, complete or verify concrete provider capability reporters and tests, and narrow `tasks/ideas-to-review/go-llm-gateway/phase-4-provider-capabilities-and-local-validation-contract.md` to remaining coverage gaps |
| `P4-API-05` | `uncertain` | remains open | Interaction cancellation, partial-output behavior, stream error classification, typed stream error payload classification, and replay mismatch typing have deterministic tests. The row remains open because direct stream serialized payloads, terminal provenance, provider/session parity, and a complete taxonomy mapping for cancellation, replay mismatch, partial output, provider close, and terminal failure still need closure evidence. | `rg -n "StreamTypeMessageEnd|StreamTypeError|Classification|NewStreamErrorValue|OutputState|ErrPartialOutput|ErrReplayMismatch|InferStream" go-agent-loop/pkg go-llm-gateway/pkg agent-cli docs`; `make typecheck`; `make test` | define stream error mapping across direct streams, interaction events, provider adapters, serialized boundaries, and session helpers; keep `tasks/ideas-to-review/go-llm-gateway/phase-4-typed-errors-and-stream-terminal-contract.md` for the unproven terminal paths |
| `P4-API-06` | `uncertain` | remains open | Gateway and session paths now reject explicitly unsupported capabilities before provider execution with `UnsupportedFeatureError`, including provider, feature, requested mode, and capability state. The row remains open because unknown fallback behavior, concrete provider coverage, interaction/inferencer seams, and fal streaming behavior still need closure decisions and public docs/examples evidence. | `rg -n "UnsupportedFeatureError|validateStatelessRequest|validateSessionConfig|unsupported|validation|capabil|feature" docs/architecture/contract-gap-audit.md go-llm-gateway/pkg go-llm-gateway/README.md`; `make typecheck`; `make test` | reconcile `P4-VALIDATION-01`, extend validation evidence through remaining seams, complete provider capability reporting where unsupported behavior is known, and narrow `tasks/ideas-to-review/go-llm-gateway/phase-4-provider-capabilities-and-local-validation-contract.md` to remaining validation gaps |
| `P4-API-07` | `uncertain` | remains open | Dependency ownership evidence now includes repaired provider HTTP runtime injection, prompt-resolution inspection through `LoadSystemPromptWithDetails`, and public migration guidance. The row remains open because exact reconciled audit rows are missing and hidden prompt, filesystem, environment, process, transport, network, time, provider runtime, and construction seams are not fully mapped to closed, injected, side-effect-free, or explicitly open status. | `rg -n "LoadSystemPromptWithDetails|buildProviderHTTPRuntime|ProviderBuildContext|WithProviderHTTPBaseTransport|P4-DI|P4-HYGIENE" docs go-agent-loop go-llm-gateway agent-cli`; `make typecheck`; `make test` | import or link the reconciled audit row list, separate closed prerequisite DI repairs from remaining hidden side effects, and continue `tasks/ideas-to-review/go-llm-gateway/phase-4-dependency-result-context-lifecycle-contract.md` for unresolved dependency surfaces |
| `P4-GATE-01` | `fail` | remains open | The merged head contains meaningful starter repairs, but multiple row-level uncertainties remain; the baseline does not yet provide enough reconciled audit, provider-coverage, stream/session, docs, examples, and credential-free command evidence to close the public API hardening gate. | `rg -n "P4-API|P4-GATE|P4-CTX|P4-ERR|P4-RESULT|P4-STREAM|P4-CAP|P4-VALIDATION|P4-DI" docs go-agent-loop go-llm-gateway agent-cli`; `make typecheck`; `make lint`; `make test` | consume this validator, queue the repair/reconciliation batches below, update audit and public guidance with row-level evidence, and rerun a validator before any next Phase 4 feature batch |

## Implementation-Ready Future Work

Every non-pass row is scoped to implementation-ready repair or reconciliation
work. Some work items are now narrower than originally written because
`origin/main` already introduced starter repairs for typed errors, stream
classification, capability discovery, local unsupported-feature validation,
result states, and dependency/result contracts:

- `tasks/ideas-to-review/go-llm-gateway/phase-4-audit-validator-015-reconciliation.md`
  restores or explicitly supersedes validator-015 and reconciles stale audit
  evidence with the implemented Phase 4 starter APIs.
- `tasks/ideas-to-review/go-llm-gateway/phase-4-typed-errors-and-stream-terminal-contract.md`
  should now focus on provider/session parity, serialized stream payloads,
  terminal provenance, and any stream/session error paths not covered by the
  current typed taxonomy.
- `tasks/ideas-to-review/go-llm-gateway/phase-4-provider-capabilities-and-local-validation-contract.md`
  should now focus on concrete provider coverage, stale audit reconciliation,
  interaction/inferencer seam evidence, unknown fallback behavior, and fal
  streaming closure decisions.
- `tasks/ideas-to-review/go-llm-gateway/phase-4-dependency-result-context-lifecycle-contract.md`
  should now focus on unresolved session send/result helpers, exact reconciled
  audit row closure, and remaining dependency side-effect mappings.

## Next Planner Action

The next planner action is `repair`.

Plan and execute the Phase 4 repair ideas above before starting any new Phase 4
feature batch. No new Phase 4 feature batch should proceed until this validator
decision is consumed, because the reviewed baseline does not yet provide public
contract evidence sufficient to mark any requested checklist row complete.

## Story 006 Closure

The publish-closure story passes for validator purposes: the final report
summarizes `P4-API-01` through `P4-API-07` and `P4-GATE-01` with verdicts,
public evidence, affected declarations from the row findings above, closure
decisions, reviewer commands, and exact future repair work for every non-pass
row. It recommends exactly one next planner action, `repair`, and explicitly
blocks the next Phase 4 feature batch until the decision is consumed.
