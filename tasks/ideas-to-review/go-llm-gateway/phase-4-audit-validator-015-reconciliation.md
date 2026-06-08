# Phase 4 audit and validator-015 reconciliation

## Problem

The Phase 4 validator PRD requires comparison against validator-015 findings,
but this checkout does not contain a committed validator-015 artifact or an
explicit supersession note. The existing architecture audit now includes Phase
4 coverage for provider capability discovery and local unsupported-feature
validation, but some audit wording is stale relative to the implemented starter
APIs and the missing validator-015 source still prevents clean provenance.

## Why it matters

- Checklist closure can be driven by stale or missing planning evidence instead
  of public API, docs, examples, and deterministic tests.
- Reviewers cannot tell whether validator-015 findings were fixed, superseded,
  or simply unavailable.
- Capability and unsupported-feature validation work can be overclaimed if
  stale audit wording is not reconciled with the implemented `CapabilityReporter`
  and `UnsupportedFeatureError` contracts.

## Suggested direction

- Restore validator-015 into committed internal docs or add a reviewer-facing
  supersession note that names the replacement evidence.
- Add a compact Phase 4 audit reconciliation table mapping every open, narrowed,
  or closed audit row to `P4-API-01` through `P4-API-07` and `P4-GATE-01`.
- Reconcile provider capability discovery and local unsupported-feature
  validation audit wording with the implemented supported, unsupported, and
  unknown capability states, gateway/session validation seams, and remaining
  concrete-provider coverage gaps.
