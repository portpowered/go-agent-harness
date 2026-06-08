# Phase 4 API Contract Validator

## Subject Under Review

This validator reviews the completed Phase 4 public API contract hardening
starter work. Run this pass only after the starter slices under review have
landed and the branch under review is intended to represent the candidate
baseline for the next Phase 4 planning decision.

The validator inspects observable repository state. It does not implement new
Phase 4 API features and does not use broad cleanup or duplicate CI coverage as
a substitute for API contract evidence.

## Scope

This validator records findings for exactly these checklist rows:

- `P4-API-01`: context usage and caller-controlled cancellation/timeout
  contracts
- `P4-API-02`: typed, caller-actionable error contracts
- `P4-API-03`: unambiguous result contracts and failure signals
- `P4-API-04`: provider capability discovery
- `P4-API-05`: stream semantics and error preservation
- `P4-API-06`: local validation of unsupported provider/request features
- `P4-API-07`: dependency injection, provider configuration, and hidden side
  effects
- `P4-GATE-01`: overall public API contract hardening gate readiness

The validator evaluates the completed starter slices for:

1. Exported API contract audit
2. Gateway error taxonomy
3. Provider capability discovery and local request validation

Validation focuses on observable API contract coherence, consumer usability,
and architecture drift across public code, docs, tests, and examples. It does
not reopen unrelated implementation design or use command success as a broad
proxy for contract correctness.

## Evidence Inputs

This convergence pass cites the following authoritative repository surfaces:

- `docs/internal/checklist.md`
- `docs/architecture/contract-gap-audit.md`
- public package documentation and package comments for changed surfaces
- tests and examples that prove public API behavior without live credentials or
  external network access
- reviewer-runnable local commands that make each reported claim reproducible

## Required Finding Shape

Use this shape for every row-level finding:

### `[Checklist Row ID]` - `[Row Area]`

- `outcome`: `pass` | `fail` | `uncertain`
- `evidence`:
- `affected files / declarations`:
- `closure decision`: `may close` | `must remain open`
- `exact repair work`:
- `reviewer commands`:

Every non-pass outcome must include exact repair work. A `pass` outcome must
include enough evidence for a reviewer to rerun or inspect the claim without
recovering planner intent from previous work items.

## Closure Rules

- A row may close only when the report ties the completed starter slices to
  observable repository evidence for that row and the evidence includes public
  consumer guidance where the changed surface is public.
- A row must remain open when evidence is missing, only inferred from intent,
  unavailable without credentials or network access, string-only where typed or
  structured behavior is required, or too broad for an implementer to repair in
  one scoped follow-up batch.
- `P4-GATE-01` may close only when the row-level report gives planners one of
  these exact next actions: `repair`, `cleanup/reconciliation`, or `next Phase
  4 feature batch`.

## Current Story Status

This document currently defines validator scope and evidence format only. Later
validator stories must fill in row-by-row outcomes, audit coverage, gateway
error taxonomy evidence, provider capability and validation evidence,
reviewer-runnable command results, closure decisions, and the final next
planner action.
