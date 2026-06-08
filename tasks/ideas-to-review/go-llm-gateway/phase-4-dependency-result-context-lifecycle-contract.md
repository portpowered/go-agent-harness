# Phase 4 Dependency, Result, Context, And Lifecycle Contract Repair

## Problem

The Phase 4 validator found that dependency ownership, result states, context
semantics, and lifecycle states are only partially converged. Some repaired
Phase 2 seams are solid, but the public API still does not expose one
reviewer-verifiable contract for cancellation versus buffer drop, empty success,
partial success, closed or drained state, replay divergence, synthesized stream
completion, prompt/filesystem side effects, and session request configuration.

## Proposed Scope

- Define an additive public outcome vocabulary for gateway, buffer, session,
  replay, and stream surfaces covering empty success, cancellation, partial
  success, closed or drained state, provider-authored completion,
  loop-synthesized completion, replay divergence, replay incomplete, and
  terminal failure.
- Replace or supplement ambiguous bool returns where callers need to distinguish
  cancellation, full buffers, closed sessions, and terminal failure.
- Define one loop-facing session request/config contract that separates
  `context.Context` lifetime from session shape.
- Split prompt assembly into pure composition plus injected filesystem, config,
  system-info, and skills loaders.
- Document each remaining filesystem, environment, process, transport, network,
  and time dependency as caller-owned, injected, side-effect free, or explicitly
  open work.
- Preserve Phase 2 ownership repairs for tool execution, stateless provider HTTP
  runtime injection, session runtime planning, and replay/record relay
  cancellation.

## Evidence To Add

- Deterministic tests for cancellation versus buffer-full or closed-state
  outcomes.
- Fake-provider stream tests that distinguish provider-authored end from
  loop-synthesized end.
- Replay tests that expose replay divergence and incomplete replay through typed
  public outcomes.
- CLI/session tests that prove timeout, cancellation, close, capture flush, and
  replay completion use the documented lifecycle states.
- Docs and audit updates mapping the repaired public declarations to
  `P4-API-01`, `P4-API-03`, `P4-API-07`, and `P4-GATE-01`.
