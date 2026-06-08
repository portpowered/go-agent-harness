# Phase 3 Shared Contract Validator

## Purpose

This validator runs after `phase-3-shared-contract-decision` completes. Its
subject under review is the delivered repository state from that completed
slice.

The validator branch, `phase-3-shared-contract-validator`, exists only to
collect and publish reviewer-facing convergence evidence for the shared
contract decision. It must not broaden into unrelated cleanup, package
inventory review, or follow-on Phase 3 redesign.

## Scope Boundaries

- Primary goal: determine whether the completed shared-contract decision now
  converges on one reviewer-verifiable repository boundary.
- Required finding groups:
  - `P3-CORE-01` authoritative shared contract boundary
  - `P3-CORE-02` truthful gateway model boundary documentation
  - `P3-CORE-05` explicit adapter composition boundaries
  - `P3-CORE-06` reviewer-verifiable dependency proof
- Missing or contradictory repository surfaces are evidence. The validator must
  not silently replace missing branch data with planner memory or inferred
  architecture intent.
- CI duplication and generic repository cleanup are out of scope unless they
  provide direct evidence for one of the four required finding groups.

## Evidence Inputs

The convergence pass must cite these repository surfaces directly when they are
present on the reviewed branch:

- `docs/internal/checklist.md`
- `tasks/todo/phase-3-shared-contract-decision.md`
- package comments, exported names, tests, and architecture docs that expose
  shared-contract ownership, gateway model ownership, adapter boundaries, or
  dependency-proof enforcement

If a required planning or checklist surface is missing from the reviewed
branch, the validator must record that absence explicitly as repository
evidence instead of reconstructing the missing input from prior discussion.

## Evidence Model

Every finding in the convergence report must use this structure:

| Field | Requirement |
| --- | --- |
| `group` | Exactly one of `P3-CORE-01`, `P3-CORE-02`, `P3-CORE-05`, or `P3-CORE-06`. |
| `subject` | The checklist row, slice commitment, package surface, document, or proof entrypoint under inspection. |
| `outcome` | Exactly one of `pass`, `fail`, or `uncertain`. |
| `evidence` | Concrete observed facts from the reviewed branch. |
| `affectedFilesOrSurfaces` | Exact files, packages, commands, reports, or reviewer-visible surfaces that support the finding. |
| `remainingDrift` | The specific contradiction, ambiguity, or missing proof that still exists. Use `none` only when the finding is fully satisfied. |
| `requiredFollowUp` | Exact repair work required before broader Phase 3 independence slices may advance. Use `none` only when the finding is fully satisfied. |

## Outcome Rules

### `pass`

Use `pass` only when the reviewed branch contains direct, reviewer-verifiable
evidence that the inspected shared-contract claim is coherent and complete.

### `fail`

Use `fail` when the reviewed branch contains direct evidence that contract
ownership, gateway model naming, adapter boundaries, or dependency proof still
contradict the Phase 3 decision or remain blocked by observable drift.

### `uncertain`

Use `uncertain` when the validator cannot verify the claim from the reviewed
branch alone. Missing authoritative planning inputs, ambiguous package
ownership, or undocumented proof behavior belong here unless the absence itself
directly contradicts a required Phase 3 claim, in which case use `fail`.

## Required Finding Groups

### `P3-CORE-01` authoritative shared contract boundary

The report must determine whether the repository exposes exactly one
authoritative shared contract boundary and whether package comments,
architecture docs, and exported naming all identify the same owner.

### `P3-CORE-02` truthful gateway model boundary documentation

The report must determine whether `go-llm-gateway/pkg/models` and related docs
truthfully describe compatibility aliases versus gateway-owned surfaces without
claiming independent shared-contract ownership.

### `P3-CORE-05` explicit adapter composition boundaries

The report must determine whether public cross-library composition still flows
through named adapter packages and whether any remaining bridge behavior is
misplaced in core packages or ambiguous ownership surfaces.

### `P3-CORE-06` reviewer-verifiable dependency proof

The report must determine whether the chosen import or architecture proof is
automated, understandable during review, and capable of catching reverse
dependency drift without introducing forbidden reverse imports itself.

## Reporting Contract

The final convergence report must:

- name `phase-3-shared-contract-decision` as the completed slice under review
- stay scoped to shared-contract convergence rather than generic cleanup
- separate findings by the four required `P3-CORE-*` groups
- use only `pass`, `fail`, or `uncertain`
- cite exact evidence and affected files or surfaces for every finding
- record `remainingDrift` and `requiredFollowUp` for every finding, using
  `none` only when no drift or repair remains
- end with one overall verdict on whether broader Phase 3 independence slices
  may advance immediately, must pause for repair, or remain blocked by
  uncertainty
