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

## Findings

### Checklist Convergence

- `outcome`: `uncertain`
- `checklist rows / commitments inspected`: `P2-COB-01`, `P2-COB-02`,
  `P2-COB-03`, `P2-COB-04`, `P2-COB-05`; planned slice commitments for
  `phase-2-constructor-ownership-boundaries`
- `affected files / surfaces`: `docs/internal/checklist.md`;
  `docs/internal/phase-2-constructor-ownership-validator.md`;
  `docs/architecture/dependencies.md`;
  `docs/architecture/contract-gap-audit.md`;
  `go-agent-loop/pkg/agentloop/agent_loop.go`;
  `go-agent-loop/pkg/agentloop/options.go`;
  `go-agent-loop/pkg/agentloop/agent_loop_test.go`;
  `agent-cli/internal/agent/provider_runtime.go`;
  `agent-cli/internal/agent/executor.go`;
  `agent-cli/internal/agent/provider_factory.go`;
  `agent-cli/internal/agent/provider_openai.go`;
  `agent-cli/internal/agent/provider_fal.go`;
  `agent-cli/internal/agent/provider_runtime_test.go`;
  `agent-cli/test/integration/provider_runtime_integration_test.go`;
  `tasks/todo/phase-2-constructor-ownership-boundaries.md` (missing)
- `evidence`: `P2-COB-01` now passes because `docs/internal/checklist.md`
  contains a dedicated constructor-ownership Phase 2 inventory that reviewers
  can cite directly during convergence validation. `P2-COB-02` passes because
  current repository state shows explicit loop-side tool capability ownership:
  `agentloop.New(...)` rejects configured tools unless the caller chooses
  `WithToolExecutor(...)` or `WithToolExecutionDisabled()`, and
  `agent_loop_test.go` covers both the constructor failure and explicit
  no-tools path. `P2-COB-03` passes because stateless live, record, and replay
  HTTP runtime policy is composed once in `agent-cli/internal/agent/
  provider_runtime.go`, injected through `ProviderBuildContext.HTTPClient`, and
  exercised by both unit tests and the provider-runtime integration test rather
  than being rebuilt inside provider constructors. `P2-COB-04` is only
  partially evidenced at the checklist-convergence layer: the repository has
  observable record/replay seam tests, but this story does not yet complete the
  dedicated runtime-consistency analysis required to determine whether every
  constructor-ownership expectation around record/replay remains satisfied.
  `P2-COB-05` is uncertain because the validator's cited slice-plan source,
  `tasks/todo/phase-2-constructor-ownership-boundaries.md`, is missing from
  the reviewed branch, so the repository does not expose one committed
  constructor-ownership planning surface that can be mapped directly to the
  delivered state. That missing source prevents a full pass for checklist
  convergence even though the implemented code and architecture docs provide
  direct evidence for the main loop-constructor and provider-runtime rows.
- `required repairs`: restore or recreate the missing committed slice-plan file
  `tasks/todo/phase-2-constructor-ownership-boundaries.md` so reviewers can map
  constructor-ownership commitments to current repository state without relying
  on inferred intent; complete the dedicated runtime-consistency finding needed
  to turn `P2-COB-04` from partial checklist evidence into a final convergence
  verdict.

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
