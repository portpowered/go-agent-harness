# Phase 4 typed errors and stream terminal contract

## Problem

The current public stream and replay surfaces expose some structured fields, but
callers still need to parse strings for many important failures. Stream `ERROR`
events are often created from `err.Error()`, replay divergence is returned as
ordinary formatted text, and stream completion does not distinguish
provider-authored terminal events from loop-synthesized `MESSAGE.END` events.

## Why it matters

- Callers cannot reliably branch on validation, unsupported feature, provider
  transport, provider stream, replay divergence, replay incomplete,
  cancellation, timeout, session close, and capture persistence failures with
  `errors.Is` or `errors.As`.
- Downstream automation can only infer replay mismatch and some session command
  failures from human-readable substrings.
- Partial output, cancellation, terminal failure, provider-authored completion,
  and loop-synthesized completion can appear identical at the stream boundary.
- Future error normalization can break existing tests and scripts unless typed
  classifications are introduced additively while preserving legacy text.

## Suggested direction

- Add exported error types or sentinels for caller-actionable gateway, provider,
  replay, validation, CLI/session, cancellation, and timeout failures.
- Preserve legacy error messages during migration, but update deterministic tests
  to assert `errors.Is` or `errors.As` for each public class.
- Extend stream `ERROR` payloads so adapters preserve structured failure class,
  provider code, parameter, event ID, retryability where applicable, and replay
  mismatch details.
- Define the public terminal-event contract for provider-authored end,
  loop-synthesized end, cancellation, replay divergence, replay incomplete,
  session close, partial output, and terminal failure.
- Document which failures are returned as Go errors and which are emitted
  in-band as stream events, then add fake-provider, replay, cancellation, and
  CLI tests that prove the contract without live credentials.
