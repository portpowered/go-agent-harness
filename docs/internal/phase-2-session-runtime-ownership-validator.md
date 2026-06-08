# Phase 2 Session Runtime Ownership Validator

## Subject Under Review

This validator reviews the completed
`phase-2-session-runtime-ownership-repair` slice. Run this pass only after
that implementation work is complete and the branch under review is intended to
represent the candidate Phase 2 baseline for the constructor-ownership lane's
session runtime behavior.

The validator inspects delivered repository state as an observable surface. It
does not reopen the implementation scope or substitute planner intent for
missing repository evidence.

## Scope

This validator records findings for exactly five groups:

1. Checklist convergence
2. Session runtime seam ownership
3. Relay cancellation contract
4. Reviewer-facing docs and audit alignment
5. Stranded queue residue

Every finding group must record:

- `outcome`: `pass`, `fail`, or `uncertain`
- supporting evidence tied to observable repository state
- affected files, runtime seams, or work IDs that reviewers can verify
- exact required follow-up repairs or exact `you work move` actions, if any

CI coverage enforcement is not a primary target for this validator. Existing
deterministic tests, quality targets, and runtime proof should be cited only
when they provide direct evidence for checklist convergence, ownership seams,
relay cancellation behavior, or reviewer-surface alignment.

## Evidence Inputs

This convergence pass cites the following authoritative repository surfaces:

- `docs/internal/checklist.md`
- `tasks/todo/phase-2-session-runtime-ownership-repair.md`
- `docs/architecture/contract-gap-audit.md`
- reviewer-facing docs that describe session record/replay runtime ownership
- code and tests that expose session runtime ownership and cancellation as
  observable behavior

If one of those cited surfaces is missing from the reviewed branch, the
validator must record `fail` or `uncertain` from that absence rather than
silently replacing it with an undocumented source.

## Required Checklist Coverage

The checklist convergence finding must inspect:

- `P2-COB-04`
- `P2-COB-05`
- `P2-GATE-01`
- the story commitments recorded in
  `tasks/todo/phase-2-session-runtime-ownership-repair.md`

The validator may cite narrower `P2-SRO-*` rows as supporting context, but the
reviewer-facing convergence verdict for this lane must map back to the
constructor-ownership checklist rows above.

## Shared Finding Template

Use this shape for every finding group:

### [Finding Group Name]

- `outcome`: `pass` | `fail` | `uncertain`
- `checklist rows / commitments inspected`:
- `affected files / surfaces / work IDs`:
- `evidence`:
- `required repairs / you work move actions`:

## Finding-Group Requirements

### Checklist Convergence

This finding group determines whether the delivered repository state satisfies
`P2-COB-04`, `P2-COB-05`, `P2-GATE-01`, and the repair-slice commitments
without relying on prior batch status alone.

### Session Runtime Seam Ownership

This finding group determines whether the scoped session-mode live, record, and
replay flows cross one explicit CLI-owned composition seam for config
resolution, dialer ownership, and provider-specific runtime injection.

The finding must call out any hidden live dependency creation, fallback dialer
behavior, or ownership leak as `fail` or `uncertain`.

### Relay Cancellation Contract

This finding group determines whether replay and record relay writes honor one
explicit caller-owned or session-owned cancellation contract instead of
switching ownership to `context.Background()` or another hidden lifetime.

The finding must compare implementation, deterministic proof, and documented
contract text rather than treating any one surface as sufficient by itself.

### Reviewer-Facing Docs and Audit Alignment

This finding group determines whether reviewer-facing docs and
`docs/architecture/contract-gap-audit.md` describe the delivered runtime
ownership model truthfully, including the scoped live dialer seam and relay
cancellation contract.

The finding must record stale, contradictory, or dead-end guidance as a review
issue even when code behavior itself is correct.

### Stranded Queue Residue

This finding group determines whether any work items still leave the
constructor-ownership lane in a stranded or partially repaired state after the
runtime-ownership repair slice landed.

The finding must name affected work IDs and record the exact required repair or
exact `you work move` action for each remaining residue item, or explicitly
state that no manual follow-up remains.

## Outcome Rules

Use `pass` when observable repository state provides direct evidence that the
inspected checklist rows or repair commitments are satisfied.

Use `fail` when observable repository state contradicts the intended ownership
model, leaves reviewer guidance stale or misleading, or shows concrete lane
residue that still blocks convergence.

Use `uncertain` when current repository state does not provide enough evidence
to verify the claim, including missing planning inputs, contradictory surfaces,
or queue state that cannot be classified safely from the available data.

## Findings

### Checklist Convergence

- `outcome`: `uncertain`
- `checklist rows / commitments inspected`: `P2-COB-04`; `P2-COB-05`;
  `P2-GATE-01`; `phase-2-session-runtime-ownership-repair-001`;
  `phase-2-session-runtime-ownership-repair-002`;
  `phase-2-session-runtime-ownership-repair-003`;
  `phase-2-session-runtime-ownership-repair-004`;
  `phase-2-session-runtime-ownership-repair-005`
- `affected files / surfaces / work IDs`: `docs/internal/checklist.md`;
  `tasks/todo/phase-2-session-runtime-ownership-repair.md`;
  `agent-cli/internal/services/session.go`;
  `agent-cli/internal/services/session_runtime.go`;
  `agent-cli/internal/services/session_test.go`;
  `go-llm-gateway/pkg/providers/grok/provider.go`;
  `go-llm-gateway/pkg/providers/grok/provider_test.go`;
  `go-llm-gateway/pkg/providers/openai/session.go`;
  `go-llm-gateway/pkg/providers/openai/session_test.go`;
  `go-llm-gateway/pkg/testing/session_record.go`;
  `go-llm-gateway/pkg/testing/session_record_test.go`;
  `go-llm-gateway/pkg/testing/session_replay.go`;
  `go-llm-gateway/pkg/testing/session_replay_test.go`;
  `docs/architecture/contract-gap-audit.md`;
  `agent-cli/docs/session-record-replay.md`;
  root `Makefile`; work IDs
  `phase-2-session-runtime-ownership-repair-001` through
  `phase-2-session-runtime-ownership-repair-005`
- `evidence`:
  - `P2-COB-04`: `fail`. The repository now exposes a dedicated session-mode
    runtime planning seam in `agent-cli/internal/services/session_runtime.go`,
    and the provider session surfaces reject a missing dialer at the provider
    boundary (`go-llm-gateway/pkg/providers/grok/provider_test.go:
    TestConnectSession_MissingDialerFailsBeforeDial` and
    `go-llm-gateway/pkg/providers/openai/session.go`). The broader checklist row
    still fails, though, because `planGrokRecordRuntime(...)` and
    `planOpenAIRecordRuntime(...)` silently create a live default through
    `factory.newDefaultLiveDialer()` when `SessionRunOptions.WebSocketDialer` is
    unset. That default creation means session record behavior is still not
    fully constrained to caller-owned runtime injection, which contradicts the
    "do not require hidden live dependency creation" portion of
    `docs/internal/checklist.md`.
  - `P2-COB-05`: `uncertain`. This validator now has an explicit session-runtime
    scope and a repository-backed checklist-convergence finding, but the
    constructor-ownership planning surface named by the broader row,
    `tasks/todo/phase-2-constructor-ownership-boundaries.md`, is still missing.
    Because that authoritative planning input is absent from the repository
    state, this branch cannot yet prove that reviewer guidance and repair
    visibility for the whole constructor-ownership lane are complete.
  - `P2-GATE-01`: `pass`. Deterministic proof exists on the cited repository
    surfaces: `agent-cli/internal/services/session_test.go` exercises session
    runtime planning, missing-dialer failures, replay routing, and cancellation;
    `go-llm-gateway/pkg/testing/session_record_test.go` proves relay writes stop
    once the owned context is canceled; and
    `go-llm-gateway/pkg/testing/session_replay_test.go` proves replay delivery
    stops once the owned replay context is canceled. The root `Makefile`
    exposes `typecheck` as the compile-validation quality gate for this
    workspace, and this validator story was checked with that command.
  - `phase-2-session-runtime-ownership-repair-001`: `pass`. The repository now
    routes session-mode config loading, replay-vs-record selection, and
    provider-specific inferencer construction through
    `agent-cli/internal/services/session_runtime.go` before provider
    construction begins, which matches the committed CLI-owned seam.
  - `phase-2-session-runtime-ownership-repair-002`: `fail`. The provider
    session implementations now consume injected dialers and fail explicitly
    when the provider receives none, but the CLI planner still creates a live
    default dialer when the caller omits one. That leaves the branch short of
    the stricter "owned dialer must already exist at the seam" commitment.
  - `phase-2-session-runtime-ownership-repair-003`: `pass`. The record and
    replay helpers now carry explicit relay lifecycle ownership through
    `WithSessionRelayContext(...)`, `WithReplayContext(...)`, and the relay
    tests that verify messages stop after cancellation instead of continuing on
    `context.Background()`.
  - `phase-2-session-runtime-ownership-repair-004`: `pass`. The repository
    contains deterministic tests for the repaired seam and cancellation contract
    on the exact surfaces named in the repair slice, and those tests do not
    require live credentials or external network access.
  - `phase-2-session-runtime-ownership-repair-005`: `pass`. The later
    `Documentation and Audit Alignment`, `Stranded Queue Residue`, and
    `Overall Convergence Verdict` finding groups are now complete in this
    report, and they confirm that the reviewer-facing docs and audit text were
    reconciled to the delivered repository truth. The aligned evidence now
    lives in `agent-cli/docs/session-record-replay.md`,
    `docs/architecture/contract-gap-audit.md`, and the validator findings
    below, which explicitly describe the remaining record-path dialer fallback
    and the missing constructor-ownership planning artifact instead of claiming
    full lane convergence.
- `required repairs / you work move actions`:
  - Move session-mode live dialer ownership from
    `session_runtime.go`'s default-dialer fallback to an explicit caller-owned
    dependency if `P2-COB-04` and repair commitment `...-002` are meant to
    converge as written.
  - Restore or replace the missing
    `tasks/todo/phase-2-constructor-ownership-boundaries.md` planning surface
    before claiming full `P2-COB-05` convergence for the broader
    constructor-ownership lane.

### Session Runtime Seam Ownership

- `outcome`: `fail`
- `checklist rows / commitments inspected`: `P2-COB-04`;
  `phase-2-session-runtime-ownership-repair-001`;
  `phase-2-session-runtime-ownership-repair-002`
- `affected files / surfaces / work IDs`:
  `agent-cli/internal/services/session_runtime.go`;
  `agent-cli/internal/services/session_test.go`;
  `go-llm-gateway/pkg/providers/grok/provider.go`;
  `go-llm-gateway/pkg/providers/grok/provider_test.go`;
  `go-llm-gateway/pkg/providers/openai/session.go`;
  `go-llm-gateway/pkg/providers/openai/session_test.go`;
  `tasks/todo/phase-2-session-runtime-ownership-repair.md`
- `evidence`:
  - `pass` evidence for the repaired seam exists at the provider boundary.
    `go-llm-gateway/pkg/providers/grok/provider.go` and
    `go-llm-gateway/pkg/providers/openai/session.go` now reject missing
    WebSocket dialers instead of constructing them internally, and
    `TestConnectSession_MissingDialerFailsBeforeDial` in both provider test
    files proves that the provider session surfaces fail before any dial
    attempt when the owned runtime dependency is absent.
  - `pass` evidence for the scoped CLI composition seam exists in the runtime
    planner shape. `planSessionRuntime(...)` routes replay, injected-live, and
    record setup through `agent-cli/internal/services/session_runtime.go`
    before provider construction, and
    `TestPlanSessionRuntime_OpenAIReplayRoutesThroughOpenAIRuntimeSeam` proves
    that OpenAI websocket replay stays on the OpenAI-specific seam rather than
    falling back to Grok-specific wiring.
  - The overall runtime ownership finding is still `fail`, because the record
    planner keeps one hidden live dependency creation path. In both
    `planGrokRecordRuntime(...)` and `planOpenAIRecordRuntime(...)`, when
    `SessionRunOptions.WebSocketDialer` is nil the planner assigns
    `factory.newDefaultLiveDialer()` before provider construction. That means
    the CLI seam still synthesizes a live transport instead of requiring the
    caller-owned dependency to cross the seam explicitly.
  - `TestPlanSessionRuntime_OpenAIRecordOwnsConfigAndDialerSelection` confirms
    the current behavior directly: the test expects the OpenAI record path to
    use a factory-owned default live dialer when none is injected. By
    contrast, `TestPlanSessionRuntime_GrokRecordPreservesCallerOwnedDialer`
    proves the seam does preserve ownership when a dialer is supplied, and
    `TestPlanSessionRuntime_RecordRejectsMissingOwnedDialer` shows the planner
    only fails when the factory cannot supply that fallback at all.
  - Against the stricter repair commitment in
    `phase-2-session-runtime-ownership-repair-002`, this repository state does
    not yet prove that scoped Grok and OpenAI record behavior is driven solely
    by explicitly injected runtime dependencies. The hidden fallback now lives
    in the CLI planner rather than the provider constructors, but it is still a
    live ownership leak for the validator scope.
- `required repairs / you work move actions`:
  - Remove the `newDefaultLiveDialer()` fallback from the record planning path
    if `P2-COB-04` and repair commitment `phase-2-session-runtime-ownership-repair-002`
    are intended to converge as written.
  - Update the session-runtime planner tests to expect explicit caller-owned
    dialer injection on record flows once that seam is repaired.

### Relay Cancellation Contract

- `outcome`: `pass`
- `checklist rows / commitments inspected`: `P2-GATE-01`;
  `phase-2-session-runtime-ownership-repair-003`;
  `phase-2-session-runtime-ownership-repair-004`
- `affected files / surfaces / work IDs`:
  `go-llm-gateway/pkg/testing/session_record.go`;
  `go-llm-gateway/pkg/testing/session_record_test.go`;
  `go-llm-gateway/pkg/testing/session_replay.go`;
  `go-llm-gateway/pkg/testing/session_replay_test.go`;
  `go-llm-gateway/pkg/testing/session_inferencer.go`;
  `agent-cli/internal/services/session.go`;
  `agent-cli/docs/session-record-replay.md`;
  `docs/architecture/contract-gap-audit.md`;
  `tasks/todo/phase-2-session-runtime-ownership-repair.md`
- `evidence`:
  - The implementation now keeps replay and record relay writes on one owned
    cancellation seam instead of switching relay delivery to
    `context.Background()`. `go-llm-gateway/pkg/testing/session_record.go`
    accepts `WithSessionRelayContext(...)`, wraps that context with
    `context.WithCancel(...)`, and writes relayed inbound messages with
    `relay.Write(rec.relayCtx, msg)`. `go-llm-gateway/pkg/testing/session_replay.go`
    mirrors that contract through `WithReplayContext(...)`,
    `context.WithCancel(...)`, and `r.outbound.Write(r.replayCtx, msg)`.
  - The owned runtime context is threaded from the session caller into both
    helper layers. `go-llm-gateway/pkg/testing/session_inferencer.go` binds the
    `ConnectSession(ctx)` lifetime to `NewSessionRecorder(..., WithSessionRelayContext(ctx))`
    and `NewSessionReplayer(..., WithReplayContext(ctx))`, while
    `agent-cli/internal/services/session.go` passes the command context into
    `gwtesting.NewSessionReplayer(...)` for transcript replay.
  - Deterministic runtime proof matches that implementation contract.
    `TestSessionRecorder_RelayStopsWhenOwnedContextCanceled` proves that the
    recorder stops forwarding and recording inbound messages after the owned
    relay context is canceled, and
    `TestSessionReplayer_StopsDeliveryWhenOwnedContextCanceled` proves that the
    replayer stops timed delivery and does not leave a queued post-cancel
    message behind.
  - Reviewer-facing docs and contract-gap audit text agree with the delivered
    behavior on this specific surface. `agent-cli/docs/session-record-replay.md`
    states that canceling the command context stops replay delivery and
    recorder relay writes at the same seam that owns runtime wiring, and
    `docs/architecture/contract-gap-audit.md` records `CTX-02` as narrowed by
    the explicit relay lifecycle context repair rather than describing a
    remaining `context.Background()` fallback.
- `required repairs / you work move actions`:
  - None for the scoped replay and record relay cancellation contract. Keep new
    session-helper wrappers bound to the caller-owned `ConnectSession(ctx)`
    lifetime so this contract does not regress.

### Reviewer-Facing Docs and Audit Alignment

- `outcome`: `pass`
- `checklist rows / commitments inspected`: `P2-SRO-04`;
  `phase-2-session-runtime-ownership-repair-005`
- `affected files / surfaces / work IDs`:
  `docs/architecture/contract-gap-audit.md`;
  `docs/architecture/dependencies.md`;
  `docs/internal/checklist.md`;
  `agent-cli/docs/session-record-replay.md`;
  `go-llm-gateway/pkg/testing/README.md`;
  `docs/internal/phase-2-session-runtime-ownership-validator.md`;
  `phase-2-session-runtime-ownership-repair-005`
- `evidence`:
  - The reviewer-facing surfaces now distinguish the repaired session-helper
    and provider-constructor behavior from the remaining CLI planner leak
    instead of collapsing them into one "resolved" claim. In
    `docs/architecture/contract-gap-audit.md`, `DI-04` now records the seam as
    narrowed rather than resolved and names the remaining
    `newDefaultLiveDialer()` fallback in
    `agent-cli/internal/services/session_runtime.go` as the concrete reason the
    broader constructor-ownership row is still open.
  - `agent-cli/docs/session-record-replay.md` now matches the delivered
    repository state: it records the explicit CLI-owned runtime seam, the
    provider-boundary missing-dialer failures, the relay cancellation contract,
    and the remaining record-path default-dialer fallback that still blocks
    full `P2-COB-04` convergence.
  - The supporting reviewer surfaces stay consistent with that narrower claim.
    `docs/internal/checklist.md` still defines `P2-SRO-04` as a reviewer-
    visible evidence obligation, `docs/architecture/dependencies.md` describes
    the session-runtime gap as narrowed rather than eliminated, and
    `go-llm-gateway/pkg/testing/README.md` documents the relay-context APIs
    without claiming broader constructor-ownership convergence.
  - No remaining stale or contradictory guidance was found on the reviewed
    surfaces after those doc corrections. The reviewer can now read the audit,
    session workflow doc, and validator report together without reconstructing
    planner intent from earlier branch history.
- `required repairs / you work move actions`:
  - None for doc truthfulness on the reviewed surfaces. Keep future reviewer
    docs aligned with the validator's narrower verdict until the remaining
    dialer fallback is actually removed.

### Stranded Queue Residue

- `outcome`: `fail`
- `checklist rows / commitments inspected`: `P2-COB-04`; `P2-COB-05`;
  `phase-2-session-runtime-ownership-repair-002`;
  `phase-2-session-runtime-ownership-repair-005`
- `affected files / surfaces / work IDs`:
  `agent-cli/internal/services/session_runtime.go`;
  `agent-cli/internal/services/session_test.go`;
  missing `tasks/todo/phase-2-constructor-ownership-boundaries.md`;
  work IDs `P2-COB-04`, `P2-COB-05`,
  `phase-2-session-runtime-ownership-repair-002`
- `evidence`:
  - One concrete constructor-ownership residue item is still stranded on the
    reviewed branch head. `agent-cli/internal/services/session_runtime.go`
    keeps the record path mergeable by synthesizing
    `factory.newDefaultLiveDialer()` when the caller omits
    `SessionRunOptions.WebSocketDialer`, and
    `TestPlanSessionRuntime_OpenAIRecordOwnsConfigAndDialerSelection` in
    `agent-cli/internal/services/session_test.go` still codifies that fallback
    as expected behavior. That leaves `P2-COB-04` and repair commitment
    `phase-2-session-runtime-ownership-repair-002` unresolved on repository
    evidence.
  - The broader constructor-ownership queue also has one missing planning
    surface. `P2-COB-05` cannot be verified cleanly because
    `tasks/todo/phase-2-constructor-ownership-boundaries.md` is absent from the
    repository, so the validator cannot prove that the broader lane-level
    reviewer workflow is fully represented in the checked-in backlog.
- `required repairs / you work move actions`:
  - Repair action: remove the record-path `newDefaultLiveDialer()` fallback
    from `agent-cli/internal/services/session_runtime.go` and update the
    session-runtime planner tests to require explicit caller-owned dialer
    injection for record mode.
  - You work move action: restore or replace
    `tasks/todo/phase-2-constructor-ownership-boundaries.md` with the current
    authoritative constructor-ownership planning surface before claiming
    `P2-COB-05` convergence for this lane.

## Overall Verdict

- `outcome`: `fail`
- `summary`:
  - `pass`: relay cancellation convergence is repository-backed, deterministic,
    and now documented truthfully on the reviewed surfaces.
  - `pass`: reviewer-facing docs and audit text now match the delivered repair
    slice without overstating constructor-ownership convergence.
  - `fail`: the session-mode record planner still creates a factory-owned live
    dialer when the caller omits one, so the broader constructor-ownership lane
    has not converged on explicit runtime ownership as written in `P2-COB-04`
    and repair commitment `phase-2-session-runtime-ownership-repair-002`.
  - `uncertain`: `P2-COB-05` remains blocked by the missing
    `tasks/todo/phase-2-constructor-ownership-boundaries.md` planning surface.
- `exact remaining actions`:
  - Remove the CLI planner's record-path default live dialer fallback and
    update the observable tests that currently expect it.
  - Restore or replace the missing constructor-ownership planning artifact so
    the broader lane can be validated from repository state.
