# Stream Terminal Contract

This document defines the additive public terminal taxonomy shared by direct
streams, loop streams, sessions, replay, and CLI stream serialization. It is the
compatibility plan for Phase 4 typed-error and terminal repairs: readable error
text stays readable, existing event types remain valid, and new callers should
branch on typed Go errors or structured terminal fields instead of parsing
strings.

## Public Fields

The shared message package owns the terminal vocabulary:

- `messages.TerminalReason` names the caller-visible terminal category.
- `messages.TerminalProvenance` identifies the layer that authored the terminal
  state.
- `messages.TerminalOutputState` identifies whether output is complete,
  partial, absent, or not applicable.

The fields are additive on terminal payloads that already cross public stream or
session boundaries:

- `MessageEndValue.terminal_reason`, `terminal_provenance`, and `output_state`
  can describe successful provider-authored or loop-synthesized completion.
- `ErrorValue.classification`, `terminal_reason`, `terminal_provenance`, and
  `output_state` can describe in-band terminal failures while preserving the
  readable `message` field.
- `SessionCloseValue.classification`, `terminal_reason`,
  `terminal_provenance`, and `output_state` can describe why a session closed
  while preserving the existing provider or implementation `reason` string.

Returned Go errors continue to use `errors.Is` or `errors.As` for typed
classification. In-band stream/session/CLI events use the structured fields
above. Some paths expose both: the returned error preserves in-process
classification and the stream event carries the serialized classification for
remote or CLI consumers.

## Terminal Categories

| Terminal reason | Caller action | Public classification surface |
| --- | --- | --- |
| `provider_authored_completion` | Treat as successful completion authored by the provider. | `MESSAGE.END` with `terminal_reason`, `terminal_provenance=provider`, and `output_state=complete`. |
| `loop_synthesized_completion` | Treat as successful completion synthesized by the loop after reconstructing output. | `MESSAGE.END` or loop result/stream outcome with `terminal_provenance=loop`; legacy synthesized boundaries remain readable and ordered. |
| `cancellation` | Stop work and handle as caller shutdown or deadline; do not retry as provider failure. | Returned errors preserve `context.Canceled` or `context.DeadlineExceeded` and may also match gateway cancellation classification; in-band errors use `ErrorValue.terminal_reason=cancellation`. |
| `replay_divergence` | Treat deterministic replay as mismatched input or fixture data. | Returned replay errors should match replay mismatch classes with `errors.Is`/`errors.As`; replay stream/session events use `terminal_reason=replay_divergence` where emitted in-band. |
| `replay_incomplete` | Treat replay as incomplete fixture/capture consumption, separate from divergence. | Returned replay errors or in-band replay events use `terminal_reason=replay_incomplete`; callers should not collapse it into provider close or cancellation. |
| `session_close` | Treat as a session lifecycle close when no more specific reason is available. | `SESSION.CLOSE.reason` remains the readable/provider reason; additive fields carry `terminal_reason=session_close` and session provenance. |
| `partial_output` | Preserve available output and handle the terminal status separately. | Loop `FinalTextResult.Partial`, `StreamOutcome.Partial`, or stream/session fields use `output_state=partial`; terminal reason may also be cancellation or failure when that is the authoritative stop cause. |
| `provider_close` | Treat as provider transport/session close without explicit provider-authored completion. | Session or stream terminal fields use `terminal_reason=provider_close`; returned setup/transport errors should preserve gateway transport or unsupported classes where applicable. |
| `terminal_failure` | Treat as non-cancellation failure; inspect classification for retry or caller fix policy. | Returned errors use typed gateway/provider/replay classes where available; in-band errors use `ErrorValue.classification` plus `terminal_reason=terminal_failure`. |

## Compatibility Path

The taxonomy is intentionally additive:

- Existing constructors still omit terminal fields, preserving older JSON event
  shapes unless a repaired path opts into terminal metadata.
- Existing human-readable strings remain in `ErrorValue.message`,
  `SessionCloseValue.reason`, and returned `err.Error()` text.
- New constructors add terminal metadata without changing event ordering or
  provider capability reporting.
- Callers that need machine-readable behavior should prefer `errors.Is`,
  `errors.As`, `ErrorValue.classification`, `terminal_reason`,
  `terminal_provenance`, and `output_state`.

Provider capability matrices are not expanded by this contract. Later repair
stories should add provider-close or unsupported-stream classification only when
the stream terminal behavior itself requires that public distinction.
