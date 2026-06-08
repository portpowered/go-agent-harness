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
- `outcome`: `pass`
- `evidence`: the reviewed branch now describes one authoritative shared
  contract boundary consistently across package surfaces, docs, and exported
  naming. `go-agent-loop/README.md` and
  `docs/architecture/dependencies.md` continue to name
  `go-agent-loop/pkg/messages` as the shared cross-module message and session
  contract owner. `go-llm-gateway/pkg/models/doc.go` and
  `go-llm-gateway/pkg/models/message.go` now state that gateway message, tool,
  and token-usage names are compatibility aliases over
  `go-agent-loop/pkg/messages`, while gateway-owned session concerns stay in
  `pkg/models` separately. `go-llm-gateway/README.md` and
  `go-llm-gateway/docs/development.md` now match that split by describing
  `pkg/models` as a mixed surface with loop-owned message aliases and
  gateway-owned session/event types rather than a second message-contract
  owner. `docs/architecture/contract-gap-audit.md` still records the historic
  naming risk, but its recommended hardening now matches the delivered branch
  state instead of contradicting it.
- `affectedFilesOrSurfaces`: `go-agent-loop/pkg/messages/agent_messages.go`;
  `go-agent-loop/pkg/messages/session.go`; `go-agent-loop/README.md`;
  `go-llm-gateway/pkg/models/doc.go`;
  `go-llm-gateway/pkg/models/message.go`; `go-llm-gateway/README.md`;
  `go-llm-gateway/docs/development.md`; `docs/architecture/dependencies.md`;
  `docs/architecture/contract-gap-audit.md`
- `remainingDrift`: none
- `requiredFollowUp`: none

### Boundary Description Cross-Check

- `group`: `P3-CORE-02`
- `subject`: whether the same repository surfaces that describe the shared
  contract boundary also describe gateway model ownership truthfully enough to
  avoid a second contract authority
- `outcome`: `pass`
- `evidence`: `go-llm-gateway/pkg/models/message.go` remains a direct alias
  layer over loop-owned message contracts, and the reviewed branch now says so
  explicitly in every relevant gateway-facing surface. The new package comment
  in `go-llm-gateway/pkg/models/doc.go` states that message, tool, and
  token-usage names follow `go-agent-loop/pkg/messages` and do not define an
  independent gateway vocabulary. `go-llm-gateway/README.md` now distinguishes
  loop-owned message aliases from gateway-owned session config and realtime
  event types, and `go-llm-gateway/docs/development.md` mirrors that same split
  for contributors. The architecture docs already described `pkg/models` as a
  non-independent contract surface, so the package docs, README, development
  guide, and architecture docs now tell one truthful story about the gateway
  model boundary.
- `affectedFilesOrSurfaces`: `go-llm-gateway/pkg/models/doc.go`;
  `go-llm-gateway/pkg/models/message.go`;
  `go-llm-gateway/pkg/models/session.go`; `go-llm-gateway/README.md`;
  `go-llm-gateway/docs/development.md`; `docs/architecture/dependencies.md`;
  `docs/architecture/contract-gap-audit.md`
- `remainingDrift`: none
- `requiredFollowUp`: none

### Adapter Explicitness

- `group`: `P3-CORE-05`
- `subject`: public cross-library adapter packages and bridge ownership between
  `go-agent-loop` and `go-llm-gateway`
- `outcome`: `pass`
- `evidence`: the reviewed branch keeps the cross-library bridge in explicit,
  named adapter packages instead of hiding it in loop core packages. The
  public adapter types remain `go-llm-gateway/pkg/inference.GatewayInferencer`
  and `go-llm-gateway/pkg/inference.SessionGatewayInferencer`, both of which
  include compile-time assertions that they satisfy the loop-owned interfaces
  `messages.Inferencer` and `messages.SessionInferencer`. Their implementations
  delegate into gateway-owned request/response and session surfaces while
  returning loop-owned message/session contracts, which keeps the composition
  boundary reviewer-visible. `docs/architecture/dependencies.md`,
  `go-llm-gateway/README.md`, and `go-llm-gateway/docs/development.md` all
  name `pkg/inference` as the intended bridge into `go-agent-loop`, and this
  branch does not expose any competing adapter ownership claim in
  `go-agent-loop/pkg/messages` or other loop core packages.
- `affectedFilesOrSurfaces`: `go-llm-gateway/pkg/inference/main_inferencer.go`;
  `go-llm-gateway/pkg/inference/session_inferencer.go`;
  `go-llm-gateway/README.md`; `go-llm-gateway/docs/development.md`;
  `docs/architecture/dependencies.md`
- `remainingDrift`: none
- `requiredFollowUp`: none

### Dependency Proof

- `group`: `P3-CORE-06`
- `subject`: reviewer-verifiable proof that `go-agent-loop` does not drift into
  a reverse dependency on `go-llm-gateway`
- `outcome`: `pass`
- `evidence`: the reviewed branch now contains a committed automated proof at
  `go-agent-loop/test/functional/dependency_direction_test.go`. Reviewers can
  run `cd go-agent-loop && go test ./test/functional -run TestDependencyDirection_GoAgentLoopDoesNotDependOnGateway`,
  which shells out to `go list -deps ./...` from the loop module root and
  fails if any compiled `go-agent-loop` package depends on
  `github.com/portpowered/go-llm-gateway`. `docs/architecture/dependencies.md`
  now cites that exact command, so the dependency rule is no longer enforced by
  architecture prose alone. This proof checks the delivered build graph without
  introducing a forbidden source import in the loop module itself.
- `affectedFilesOrSurfaces`: `go-agent-loop/test/functional/dependency_direction_test.go`;
  `docs/architecture/dependencies.md`; root `Makefile`;
  `docs/internal/phase-3-shared-contract-validator.md`
- `remainingDrift`: none
- `requiredFollowUp`: none

## Convergence Verdict

- `overallOutcome`: `uncertain`
- `summary`: the reviewed branch now converges on one truthful shared-contract
  description for `go-agent-loop/pkg/messages` and the `go-llm-gateway/pkg/models`
  compatibility layer, keeps cross-library composition in explicit adapter
  packages, and now includes an automated reverse-dependency proof reviewers
  can run directly. The validator still cannot produce a fully authoritative
  end-state verdict because the required
  `tasks/todo/phase-3-shared-contract-decision.md` planning surface is absent,
  so the completed findings still cannot be mapped back to the committed slice
  acceptance source from the reviewed branch alone.
- `broaderPhase3Readiness`: broader Phase 3 independence slices should pause
  pending restoration of the missing Phase 3 decision-plan source on the
  reviewed branch.
