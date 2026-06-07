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
- `checklist rows / commitments inspected`: pending story 002
- `affected files / surfaces`: `docs/internal/checklist.md`,
  `tasks/todo/phase-2-session-fixture-ownership-boundary.md`
- `evidence`: story 001 defines the evidence model only; repository-state
  inspection is deferred to the checklist convergence pass
- `required repairs`: complete story
  `phase-2-session-fixture-ownership-validator-002`

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
- `summary`: story 001 establishes the validator scope and evidence model but
  does not yet claim checklist, ownership-boundary, or fixture-consistency
  convergence outcomes
- `required repairs before next Phase 2 slice`: complete stories 002 through 005
  and record reviewer-verifiable evidence for each finding group
