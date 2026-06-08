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

## Findings

### Checklist Convergence

- `group`: `P3-CORE-01`
- `subject`: authoritative Phase 3 checklist rows plus the committed
  `phase-3-shared-contract-decision` slice-plan source
- `outcome`: `uncertain`
- `evidence`: `docs/internal/checklist.md` now contains explicit `P3-CORE-01`
  through `P3-CORE-06` rows that reviewers can cite directly. That resolves
  the validator's missing-checklist problem. The branch still does not contain
  the required planning surface `tasks/todo/phase-3-shared-contract-decision.md`,
  so this validator cannot map the delivered repository state back to the
  committed slice acceptance source without relying on inferred history.
- `affectedFilesOrSurfaces`: `docs/internal/checklist.md`;
  `tasks/todo/phase-3-shared-contract-decision.md` (missing);
  `docs/internal/phase-3-shared-contract-validator.md`
- `remainingDrift`: the checklist source is now authoritative, but the branch
  still lacks the committed `phase-3-shared-contract-decision` slice-plan file
  this validator is required to cite.
- `requiredFollowUp`: restore or recreate
  `tasks/todo/phase-3-shared-contract-decision.md` on the reviewed branch so
  reviewers can map checklist rows and delivered repository evidence back to
  the committed Phase 3 decision slice without reconstructing prior branch
  history.

### Authoritative Shared Contract Boundary

- `group`: `P3-CORE-01`
- `subject`: repository-wide ownership claim for the shared message contract
  boundary
- `outcome`: `fail`
- `evidence`: the repository does not yet describe one authoritative shared
  contract boundary consistently across package surfaces, docs, and exported
  names. `go-agent-loop/README.md` and `docs/architecture/dependencies.md`
  both state that `go-agent-loop/pkg/messages` owns the shared cross-module
  message and session contracts. `go-llm-gateway/pkg/models/message.go`
  reinforces that code-level ownership by aliasing loop message types directly
  from `go-agent-loop/pkg/messages` and describing them as a single source of
  truth re-export. But `go-llm-gateway/README.md` still presents `pkg/models`
  as a primary consumer-facing package for "building messages and session
  config values that flow through the gateway", and
  `go-llm-gateway/docs/development.md` says `pkg/models/` "owns shared model
  and session types, including re-exports from `go-agent-loop`." Those gateway
  surfaces still read as a second ownership claim rather than a narrow
  compatibility alias over the loop-owned shared contract. The architecture
  audit itself also records that `pkg/models` remains a naming facade over
  loop-owned message contracts, which confirms the reviewed branch has not yet
  converged on one unmistakable authority.
- `affectedFilesOrSurfaces`: `go-agent-loop/pkg/messages/agent_messages.go`;
  `go-agent-loop/pkg/messages/session.go`; `go-agent-loop/README.md`;
  `go-llm-gateway/pkg/models/message.go`; `go-llm-gateway/README.md`;
  `go-llm-gateway/docs/development.md`; `docs/architecture/dependencies.md`;
  `docs/architecture/contract-gap-audit.md`
- `remainingDrift`: `go-agent-loop/pkg/messages` is the actual type authority,
  but gateway package naming and docs still let reviewers or downstream
  consumers read `go-llm-gateway/pkg/models` as a competing shared-contract
  owner.
- `requiredFollowUp`: update `go-llm-gateway/pkg/models` package comments and
  gateway-facing docs so they explicitly describe loop-message exports as
  compatibility aliases over `go-agent-loop/pkg/messages`, keep gateway-owned
  session-specific surfaces clearly separate, and remove wording that implies
  `pkg/models` owns an independent shared message vocabulary.

### Boundary Description Cross-Check

- `group`: `P3-CORE-02`
- `subject`: whether the same repository surfaces that describe the shared
  contract boundary also describe gateway model ownership truthfully enough to
  avoid a second contract authority
- `outcome`: `fail`
- `evidence`: the current branch already exposes enough evidence to show that
  gateway model documentation remains part of the boundary drift. The same
  surfaces that should confirm one contract owner instead split the story:
  `go-llm-gateway/pkg/models/message.go` is an alias layer over loop-owned
  message contracts, while `go-llm-gateway/README.md` and
  `go-llm-gateway/docs/development.md` still describe `pkg/models` in ownership
  language broad enough to read as a gateway-owned shared vocabulary. Because
  those surfaces are part of the repository's boundary description, the branch
  does not yet truthfully satisfy the shared-surface intent behind
  `P3-CORE-02`.
- `affectedFilesOrSurfaces`: `go-llm-gateway/pkg/models/message.go`;
  `go-llm-gateway/pkg/models/session.go`; `go-llm-gateway/README.md`;
  `go-llm-gateway/docs/development.md`; `docs/architecture/dependencies.md`;
  `docs/architecture/contract-gap-audit.md`
- `remainingDrift`: gateway docs still mix alias-layer message exports with
  genuinely gateway-owned session types under one `pkg/models` ownership label.
- `requiredFollowUp`: complete the dedicated `P3-CORE-02` validator pass by
  splitting loop-owned compatibility aliases from gateway-owned session types
  in reviewer-facing documentation and confirming every affected gateway surface
  describes that boundary consistently.

## Convergence Verdict

- `overallOutcome`: `fail`
- `summary`: the validator now has authoritative Phase 3 checklist rows, but
  the reviewed branch still fails the shared-boundary convergence check. The
  actual type authority remains `go-agent-loop/pkg/messages`, yet gateway
  package naming and docs still describe `go-llm-gateway/pkg/models` broadly
  enough to read as a second shared-contract owner. The required
  `phase-3-shared-contract-decision` planning surface is also absent, which
  leaves checklist-to-commitment mapping uncertain even after the checklist
  source was repaired.
- `broaderPhase3Readiness`: broader Phase 3 independence slices should pause
  for repair until the repository exposes one consistent contract authority and
  the missing Phase 3 decision-plan source is restored on the reviewed branch.
