# Phase 2 Session Fixture Ownership Validator

## Subject Under Review

This validator reviews the completed `phase-2-session-fixture-ownership-boundary`
slice. Run this pass only after that implementation work is complete and the
branch under review is intended to represent the candidate Phase 2 baseline for
session fixture ownership.

The validator does not reopen the implementation scope. It inspects the
delivered repository state as an observable surface and records convergence
evidence for reviewers.

## Scope

This validator produces findings for exactly three areas:

1. Checklist convergence
2. Ownership-boundary architecture drift
3. Replay and session-validation fixture consistency

Every finding in those areas must record:

- `outcome`: `pass`, `fail`, or `uncertain`
- supporting evidence tied to observable repository state
- affected files, fixture roots, or other reviewer-verifiable surfaces
- required follow-up repairs, if any

CI coverage enforcement is not a primary target for this validator. Existing
deterministic replay behavior, fixture validation behavior, or similar checks
may be cited only when they are direct evidence for convergence of the ownership
model.

## Evidence Inputs

The validator should prefer these sources when they exist in the checkout:

- `docs/internal/checklist.md` for the active Phase 2 work inventory
- `tasks/todo/phase-2-session-fixture-ownership-boundary.md` for slice
  commitments
- committed fixture roots, helper APIs, tests, and docs that define ownership or
  consumer boundaries

If one of the expected planning surfaces is missing from the repository state,
record that gap as evidence and classify the impacted finding as `uncertain`
rather than assuming the missing source still exists elsewhere.

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
- `checklist rows / commitments inspected`: the Phase 2 inventory rows that the
  PRD says live in `docs/internal/checklist.md`; the ownership-boundary slice
  commitments that the PRD says live in
  `tasks/todo/phase-2-session-fixture-ownership-boundary.md`
- `affected files / surfaces`: missing `docs/internal/checklist.md`; missing
  `tasks/todo/phase-2-session-fixture-ownership-boundary.md`; `prd.json`;
  `progress.txt`; `docs/internal/phase-2-session-fixture-ownership-validator.md`;
  `go-llm-gateway/pkg/testing/session-fixture-authoring.md`;
  `agent-cli/docs/session-record-replay.md`
- `evidence`: the repository checkout does not contain either authoritative
  planning surface named by the PRD, so the validator cannot inspect the actual
  checklist rows or the actual ownership-boundary task commitments from the
  repository state under review. That missing evidence is itself observable
  repository evidence against convergence because the validator contract depends
  on those files being reviewer-verifiable inputs. The checkout does contain the
  validator report scaffold plus fixture-related docs in
  `go-llm-gateway/pkg/testing/session-fixture-authoring.md` and
  `agent-cli/docs/session-record-replay.md`, which confirms that fixture
  ownership and replay guidance exists, but those surfaces do not map back to
  the specific Phase 2 checklist rows or slice commitments that reviewers were
  instructed to validate. Because the authoritative inventory and slice-plan
  surfaces are absent, no row-level or commitment-level convergence claim can be
  marked `pass`, and no unmet commitment can be marked `fail` with the required
  source-of-truth citation.
- `required repairs`: restore or commit `docs/internal/checklist.md` and
  `tasks/todo/phase-2-session-fixture-ownership-boundary.md` to the branch, or
  replace them with clearly documented successor surfaces that the validator can
  cite directly for row-by-row and commitment-by-commitment convergence review

### Ownership-Boundary Architecture Drift

- `outcome`: `uncertain`
- `checklist rows / commitments inspected`: pending story 003
- `affected files / surfaces`: committed `.session.json` fixture roots,
  ownership docs, replay and validation helper boundaries
- `evidence`: story 001 defines the evidence model only; ownership-boundary
  inspection is deferred to the architecture validation pass
- `required repairs`: complete story
  `phase-2-session-fixture-ownership-validator-003`

### Replay and Session-Validation Fixture Consistency

- `outcome`: `uncertain`
- `checklist rows / commitments inspected`: pending story 004
- `affected files / surfaces`: replay consumers, session-validation targets, and
  fixture provenance docs
- `evidence`: story 001 defines the evidence model only; fixture-consistency
  inspection is deferred to the consistency validation pass
- `required repairs`: complete story
  `phase-2-session-fixture-ownership-validator-004`

## Convergence Verdict

- `overall outcome`: `uncertain`
- `summary`: the validator now records a concrete checklist-convergence finding:
  the required planning inputs named by the PRD are missing from the repository
  state, so the ownership-boundary slice cannot yet be validated against the
  promised Phase 2 inventory rows or slice commitments. Ownership-boundary and
  fixture-consistency findings remain deferred.
- `required repairs before next Phase 2 slice`: restore or replace the missing
  authoritative planning surfaces for checklist validation, then complete
  stories 003 through 005 and record reviewer-verifiable evidence for the
  remaining finding groups
