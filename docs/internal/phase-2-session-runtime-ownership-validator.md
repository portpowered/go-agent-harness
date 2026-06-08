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
  - `phase-2-session-runtime-ownership-repair-005`: `uncertain`. The reviewer
    docs and audit surfaces were updated to describe the intended ownership
    model, but this validator branch has not yet completed the later finding
    groups that must confirm those surfaces are fully aligned with delivered
    behavior and residue state.
- `required repairs / you work move actions`:
  - Move session-mode live dialer ownership from
    `session_runtime.go`'s default-dialer fallback to an explicit caller-owned
    dependency if `P2-COB-04` and repair commitment `...-002` are meant to
    converge as written.
  - Restore or replace the missing
    `tasks/todo/phase-2-constructor-ownership-boundaries.md` planning surface
    before claiming full `P2-COB-05` convergence for the broader
    constructor-ownership lane.
  - Finish the remaining validator finding groups before treating the docs and
    audit refresh commitment `...-005` as fully satisfied.
