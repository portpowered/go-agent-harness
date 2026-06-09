# Phase 4 typed errors and stream terminal contract

## Problem

The current public stream, replay, provider, and gateway surfaces now expose a
starter typed-error taxonomy and structured classifications. That repaired
representative behavior still needs provider/session parity and terminal stream
closure evidence before the broader Phase 4 rows can close. Remaining gaps are
concentrated in serialized direct stream payloads, provider/session adapter
coverage, terminal provenance, and a complete mapping of which failures are
in-band stream events versus returned Go errors.

## Why it matters

- The current taxonomy must stay additive while remaining provider families and
  session/replay paths are brought into parity.
- Serialized direct stream payloads and session helpers still need a clear
  public mapping for cancellation, replay mismatch, partial output, provider
  close, terminal failure, provider-authored completion, and loop-synthesized
  completion.
- Downstream automation should be able to use `errors.Is`, `errors.As`, or
  documented structured event fields without falling back to substrings for any
  representative public path.

## Suggested direction

- Reconcile current typed-error audit findings with the implemented gateway and
  provider taxonomy.
- Extend classification tests and docs to any remaining provider, direct stream,
  session, replay, and CLI paths not covered by the starter evidence.
- Define the public terminal-event contract for provider-authored end,
  loop-synthesized end, cancellation, replay divergence, replay incomplete,
  session close, partial output, and terminal failure.
- Document which failures are returned as Go errors and which are emitted
  in-band as stream events, then add fake-provider, replay, cancellation, and
  CLI tests for any unproven terminal path without live credentials.
