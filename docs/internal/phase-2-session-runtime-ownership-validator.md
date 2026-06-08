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
