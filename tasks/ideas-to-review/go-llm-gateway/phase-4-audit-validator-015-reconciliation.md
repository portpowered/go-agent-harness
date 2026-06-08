# Phase 4 audit and validator-015 reconciliation

## Problem

The Phase 4 validator PRD requires comparison against validator-015 findings,
but this checkout does not contain a committed validator-015 artifact or an
explicit supersession note. The existing architecture audit also maps several
context, typed-error, lifecycle, dependency, and documentation gaps, but it does
not yet include explicit Phase 4 audit rows for provider capability discovery or
local unsupported-feature validation.

## Why it matters

- Checklist closure can be driven by stale or missing planning evidence instead
  of public API, docs, examples, and deterministic tests.
- Reviewers cannot tell whether validator-015 findings were fixed, superseded,
  or simply unavailable.
- Capability and unsupported-feature validation work can be overclaimed because
  no audit row currently records the full `P4-API-04` and `P4-API-06`
  vocabulary.

## Suggested direction

- Restore validator-015 into committed internal docs or add a reviewer-facing
  supersession note that names the replacement evidence.
- Add a compact Phase 4 audit reconciliation table mapping every open, narrowed,
  or closed audit row to `P4-API-01` through `P4-API-07` and `P4-GATE-01`.
- Add missing audit rows for provider capability discovery and local
  unsupported-feature validation, including the supported, unsupported, and
  unknown capability states expected by the public API contract.
