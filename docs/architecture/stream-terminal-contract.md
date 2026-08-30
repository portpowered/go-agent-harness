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

## Landed Surface Map

The reconciled contract is representative and additive. These public surfaces
currently carry the taxonomy without requiring callers to parse operator text:

| Surface | Returned Go error | In-band or serialized terminal fields |
| --- | --- | --- |
| Gateway/provider setup, validation, and stream-open failures | `gateway.ErrAuthentication`, `ErrAuthorization`, `ErrRateLimit`, `ErrInvalidRequest`, `ErrUnsupportedModel`, `ErrProviderHTTPStatus`, `ErrTransport`, `ErrCancellation`, and structured `errors.As` details where available. | Not applicable unless a stream has already started. |
| Direct gateway stream `ERROR` events | In-process `messages.ErrorValue.Err` preserves typed classes when the caller receives the Go value directly. | `classification`, `terminal_reason`, `terminal_provenance`, and `output_state` serialize on `ERROR`. |
| Loop `FinalText()` and `Stream.Outcome()` | Outcome errors preserve cancellation, deadline, or terminal-failure cause where observable. | `MESSAGE.END` and `ERROR` payloads expose terminal reason/provenance/output state; outcomes expose final status and partial-output state. |
| Session close and session error events | Session connection or setup failures return Go errors; active session terminal states are event based. | `SESSION.CLOSE` and session `ERROR` payloads expose classification, terminal reason, provenance, and output state where the provider surface emits terminal metadata. |
| Session replay outcomes | Replay errors match `gateway.ErrReplayMismatch`, `gateway.ErrReplayIncomplete`, provider replay sentinels, or caller cancellation. | Replay/session/CLI surfaces expose replay status or terminal fields where events are emitted. |
| CLI stream/session output | Command setup failures return CLI errors; active stream/session events are rendered. | NDJSON preserves payload fields, and `agent session` renders additive key/value terminal lines for session close and error metadata. |

The exact representative proof is recorded in
`docs/internal/phase-4-typed-terminal-authoritative-reconciliation.md`.
Provider-wide parity for every adapter, parser failure, and session helper is
outside this contract note unless a path is explicitly listed as landed there.

## CLI Session Output

`agent session --replay` and session record/replay loop output keep the legacy
human-readable close line:

```text
[session closed: <reason>]
```

When the underlying `SESSION.CLOSE` payload carries terminal metadata, the CLI
also emits an additive machine-readable line:

```text
[session terminal: classification=<class> terminal_reason=<reason> terminal_provenance=<layer> output_state=<state>]
```

Session `ERROR` payloads returned by the CLI keep the readable
`session error: <message>` prefix and append the same key/value terminal fields
when present. Consumers that need automation should read those key/value fields
or the underlying stream JSON event fields instead of parsing the readable
message or close reason.

## Terminal Categories

| Terminal reason | Caller action | Public classification surface |
| --- | --- | --- |
| `provider_authored_completion` | Treat as successful completion authored by the provider. | `MESSAGE.END` with `terminal_reason`, `terminal_provenance=provider`, and `output_state=complete`. |
| `loop_synthesized_completion` | Treat as successful completion synthesized by the loop after reconstructing output. | `MESSAGE.END` or loop result/stream outcome with `terminal_provenance=loop`; legacy synthesized boundaries remain readable and ordered. |
| `cancellation` | Stop work and handle as caller shutdown or deadline; do not retry as provider failure. | Returned errors preserve `context.Canceled` or `context.DeadlineExceeded` and may also match gateway cancellation classification; in-band errors use `ErrorValue.classification=cancellation` and `ErrorValue.terminal_reason=cancellation`. |
| `replay_divergence` | Treat deterministic replay as mismatched input or fixture data. | Returned replay divergence errors match `gateway.ErrReplayMismatch` or `providers.ErrReplayMismatch` and expose typed details with `errors.As`; replay stream/session events use `terminal_reason=replay_divergence` where emitted in-band. |
| `replay_incomplete` | Treat replay as incomplete fixture/capture consumption, separate from divergence. | Returned incomplete replay errors match `gateway.ErrReplayIncomplete` or `providers.ErrReplayIncomplete`; in-band replay events use `terminal_reason=replay_incomplete` where emitted. Callers should not collapse it into replay mismatch, provider close, or cancellation. |
| `replay_complete` | Treat a capture-derived replay that consumed every ordered event as successful completion. | The CLI emits `SESSION.CLOSE`-shaped terminal fields with `classification=replay_complete`, `terminal_provenance=replay`, and `output_state=complete`, followed by its visible replay-complete marker. |
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

## Cancellation And Partial Output

Caller cancellation is distinct from provider rejection, provider close, replay
mismatch, replay incomplete, and terminal failure. Loop-owned stream
cancellation is emitted in-band as an `ERROR` payload with
`classification=cancellation`, `terminal_reason=cancellation`, and
`terminal_provenance=loop` when a per-request execution is cancelled while the
runner remains active. The in-process `ErrorValue.Err` preserves
`context.Canceled` or `context.DeadlineExceeded` for callers that receive the
typed value directly.

When caller-visible output was already emitted before cancellation or terminal
failure, the terminal payload preserves that distinction with
`output_state=partial`. Consumers should keep the available deltas and branch on
`terminal_reason` for the stop cause instead of treating partial output as its
own failure class.
