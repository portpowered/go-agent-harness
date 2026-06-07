# Phase 2 Constructor Ownership Validator

## Subject Under Review

This validator reviews the completed
`phase-2-constructor-ownership-boundaries` slice. Run this pass only after that
implementation work is complete and the branch under review is intended to
represent the candidate Phase 2 baseline for constructor ownership.

The validator inspects the delivered repository state as an observable surface.
It does not reopen the implementation scope.

## Scope

This validator records findings for exactly three areas:

1. Checklist convergence
2. Constructor-ownership architecture drift
3. Record/replay runtime consistency

Every finding records:

- `outcome`: `pass`, `fail`, or `uncertain`
- supporting evidence tied to observable repository state
- affected files, runtime seams, or other reviewer-verifiable surfaces
- required follow-up repairs, if any

CI coverage enforcement is not a primary target for this validator. Existing
deterministic record/replay behavior, quality targets, or targeted runtime tests
are cited only where they provide direct convergence evidence for the ownership
model.

## Evidence Inputs

This convergence pass cites the following authoritative repository surfaces:

- `docs/internal/checklist.md`
- `tasks/todo/phase-2-constructor-ownership-boundaries.md` when present as the
  committed slice-plan source
- loop constructor, provider runtime, and record/replay surfaces that expose
  constructor ownership as observable repository behavior

If `tasks/todo/phase-2-constructor-ownership-boundaries.md` is missing from the
reviewed branch, the validator must record that as stale or missing planning
guidance instead of silently substituting an undocumented source.

## Shared Finding Template

Use this shape for every finding group:

### [Finding Group Name]

- `outcome`: `pass` | `fail` | `uncertain`
- `checklist rows / commitments inspected`:
- `affected files / surfaces`:
- `evidence`:
- `required repairs`:

## Finding Groups

### Checklist Convergence

Validate the delivered constructor-ownership slice against the relevant Phase 2
checklist rows and the slice commitments recorded for
`phase-2-constructor-ownership-boundaries`. The finding must state whether the
repository exposes enough evidence to verify each cited row or commitment from
current repository state.

### Constructor-Ownership Architecture Drift

Validate whether loop construction makes tool capability ownership explicit,
whether provider runtime wiring crosses one intentional live/record/replay seam,
and whether any hidden live dependency creation remains in constructor or
provider build paths.

### Record/Replay Runtime Consistency

Validate whether record and replay behavior still run through the intended
injected runtime dependencies after the ownership cleanup, and whether the
observable runtime and test surfaces remain aligned with the explicit ownership
model without broadening into duplicate CI review.

## Outcome Rules

Use `pass` when the reviewed repository state provides direct evidence that the
inspected checklist rows or ownership commitments are satisfied.

Use `fail` when observable repository state contradicts the intended ownership
model, preserves hidden live dependency creation, or leaves the next Phase 2
slice blocked on a concrete repair.

Use `uncertain` when the validator cannot verify the claim from current
repository state alone, including cases where planning inputs, checklist rows,
or runtime/documentation evidence are missing or contradictory.

## Dead-End and Stale Guidance Handling

The validator must record dead-end docs, stale architecture references, and
contradictory ownership guidance when those issues materially affect reviewer
understanding of loop constructor ownership, provider runtime seams, or
record/replay runtime ownership.

Missing planning inputs are evidence. The validator must not replace a missing
committed slice-plan file with inferred intent from unrelated docs without
stating that the original cited source is absent.

## Final Report Requirements

The final convergence report must:

- conclude with one overall Phase 2 constructor-ownership verdict
- summarize the supporting `pass`, `fail`, and `uncertain` findings
- list every remaining repair item required before the next Phase 2
  API-hardening slice may begin
- remain reviewer-verifiable from current repository state without requiring
  broad source archaeology or reconstruction of prior batch history
