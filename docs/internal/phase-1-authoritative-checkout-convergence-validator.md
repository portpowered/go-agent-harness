# Phase 1 Authoritative Checkout Convergence Validator

## Purpose

This validator runs after `phase-1-authoritative-checkout-reconciliation` completes.
Its subject under review is the repaired reconciliation branch,
`phase-1-authoritative-checkout-reconciliation`.

The validator branch, `phase-1-authoritative-checkout-convergence-validator`, exists
only to collect and publish reviewer-facing validation evidence for that repaired
branch. It must not advance Phase 2 work or broaden into unrelated cleanup.

## Scope Boundaries

- Primary goal: determine whether the repaired branch exposes one coherent,
  reviewer-ready Phase 1 baseline.
- Required finding groups:
  - checklist convergence
  - architecture drift
  - mergeability and reviewer readiness
- CI duplication is not a primary review target. CI entrypoints are inspected only
  as one surface of the overall Phase 1 baseline and only to determine whether they
  agree with the repaired branch's documented contract.
- Missing or contradictory repository surfaces are evidence. The validator must not
  silently substitute prior batch memory or inferred intent for missing branch data.

## Evidence Model

Every finding in the convergence report must use this structure:

| Field | Requirement |
| --- | --- |
| `group` | One of `checklist-convergence`, `architecture-drift`, or `mergeability-reviewer-readiness`. |
| `subject` | The repaired branch surface, checklist row, document, branch comparison, or workflow entrypoint under inspection. |
| `outcome` | Exactly one of `pass`, `fail`, or `uncertain`. |
| `evidence` | Concrete observed facts from the repaired branch. |
| `affectedFilesOrSurfaces` | Exact files, directories, branches, commands, or repository surfaces that support the finding. |
| `requiredFollowUp` | Observable repair work required before Phase 2 may begin. Use `none` only when the finding is fully satisfied. |

## Outcome Rules

### `pass`

Use `pass` only when the repaired branch contains direct, reviewer-verifiable
evidence that the inspected subject is coherent and complete for Phase 1.

### `fail`

Use `fail` when the repaired branch contains direct evidence of contradiction,
drift, missing required behavior, or unresolved split-brain state that blocks
reviewer confidence or Phase 2 entry.

### `uncertain`

Use `uncertain` when the validator cannot verify the claim from the repaired
branch alone. Missing authoritative surfaces, missing branch comparisons, or
missing documentation belong here unless the absence itself directly contradicts a
required Phase 1 baseline claim, in which case use `fail`.

## Required Finding Groups

### Checklist convergence

The report must inspect the relevant Phase 1 inventory and required outcomes from
`docs/internal/checklist.md` against the repaired branch.

If `docs/internal/checklist.md` is absent in the repaired branch, the report must
record that absence explicitly as branch evidence. It must not reconstruct the
checklist from memory.

### Architecture drift

The report must compare the repaired branch across these repository surfaces:

- root workspace contract such as root-level repository files and workspace layout
- README set
- CI entrypoints
- dependency or audit documents that describe the Phase 1 baseline

The validator should record contradictions, omissions, and stale descriptions, but
it should not expand into general CI redesign.

### Mergeability and reviewer readiness

The report must decide whether the repaired branch is reviewer-ready and whether
the earlier split-brain between local `main`, `origin/main`, and the prior
convergence branch is resolved, unresolved, or only partially documented.

## Reporting Contract

The final convergence report must:

- name `phase-1-authoritative-checkout-reconciliation` as the branch under review
- separate findings by the three required groups
- use only `pass`, `fail`, or `uncertain`
- cite exact evidence and affected files or surfaces for every finding
- list concrete follow-up repairs for every `fail` or `uncertain` result
- end with one overall verdict on whether Phase 2 may begin
