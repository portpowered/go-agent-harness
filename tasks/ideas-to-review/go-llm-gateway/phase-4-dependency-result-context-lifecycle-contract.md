# Phase 4 Dependency, Result, Context, And Lifecycle Contract Repair

## Problem

The Phase 4 validator found that dependency ownership, result states, context
semantics, and lifecycle states are only partially converged. Some repaired
Phase 2 seams and Phase 4 starter result/context APIs are solid, including
additive result-state and stream-outcome evidence. The public API still needs
reconciled row closure for remaining ambiguous helpers, session request
configuration, prompt/filesystem side effects, and dependency ownership
surfaces that are not fully mapped as closed, injected, side-effect free, or
explicitly open.

## Proposed Scope

- Reconcile the exact audit row list with the implemented result/context APIs,
  including `ExecuteResult.FinalText`, `Stream.Outcome`, and
  `TypedBuffer.ReadContext`.
- Replace or supplement remaining ambiguous bool returns where callers need to
  distinguish cancellation, full buffers, closed sessions, and terminal failure,
  especially session send-style helpers.
- Define one loop-facing session request/config contract that separates
  `context.Context` lifetime from session shape.
- Decide whether prompt assembly needs a library-grade pure composition API in
  addition to the current inspected CLI prompt-resolution contract.
- Document each remaining filesystem, environment, process, transport, network,
  and time dependency as caller-owned, injected, side-effect free, already
  repaired, or explicitly open work.
- Preserve Phase 2 ownership repairs for tool execution, stateless provider HTTP
  runtime injection, session runtime planning, and replay/record relay
  cancellation.

## Evidence To Add

- Deterministic tests for cancellation versus buffer-full or closed-state
  outcomes on remaining ambiguous helpers.
- Fake-provider stream tests that distinguish provider-authored end from
  loop-synthesized end.
- Replay tests that expose replay divergence and incomplete replay through typed
  public outcomes.
- CLI/session tests that prove timeout, cancellation, close, capture flush, and
  replay completion use the documented lifecycle states.
- Docs and audit updates mapping the repaired public declarations to
  `P4-API-01`, `P4-API-03`, `P4-API-07`, and `P4-GATE-01`.
