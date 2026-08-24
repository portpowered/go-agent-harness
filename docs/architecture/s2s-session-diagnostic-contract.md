# S2S Session Diagnostic Contract

This document is the operator-facing contract for diagnosing a failed
speech-to-speech session run from **logs and metrics alone**, without reading
code or re-running anything. It names, per closed-set failure mode, the first
fields a responder looks at, in order; the full stable field reference; and the
mapping between responder vocabulary ("the user's microphone audio") and the
concrete metrics series names.

Every field name and value shown below equals an observed value from the proof
suite `agent-cli/internal/services/session_diagnostics_test.go`, which drives
the real `RunSession` pipeline over the replay transport against committed
fixtures in `go-llm-gateway/pkg/testing/testdata/session-fixtures/`.

## Scope of the claim

Reconstruction of WHICH failure occurred and WHERE it happened in the turn
sequence relies on the emitted diagnostic log records plus the final metrics
snapshot — nothing else. The claim is guarded by a negative control kept as a
failing-control demonstration inside the suite
(`TestSessionDiagnostics_ZeroTurnDeadSessionFailsEveryDiagnosis`): the identical
assertion set FAILS against a committed zero-turn dead-session capture
(`session_dead_zeroturn.session.json`), so a dead or empty run cannot masquerade
as a healthy one or as any known failure mode. Pairwise distinctness of all five
signatures and non-match of a healthy multi-turn run are asserted in
`TestSessionDiagnostics_FailureModeSignaturesArePairwiseDistinct` and
`TestSessionDiagnostics_HealthyRunMatchesNoFailureSignature`.

## Where diagnostics are emitted

Injection follows the established `SessionInferencer`/`WebSocketDialer`
precedent: optional seams on `agent-cli/internal/services.SessionRunOptions`.
With no sink injected, runtime behavior and output are byte-for-byte unchanged.

| Seam | Type | Emits |
| --- | --- | --- |
| `SessionRunOptions.Diagnostics` | `services.SessionDiagnosticSink` | One canonical structured record per event (below). |
| `SessionRunOptions.MetricsRecorder` | `go-agent-loop/pkg/metrics.Recorder` | Per-direction, per-modality byte observations. |
| `SessionRunOptions.AudioInputs` | `[]services.SessionAudioInput` | User-audio injection through the loop's existing `AgentLoop.SendAudioInput` seam, attributed to a turn. |

Canonical record events (`Event` field values):

- `session_failure` — exactly one per terminal session failure. Clean runs emit
  nothing.
- `session_turn_completed` — one per completed assistant turn
  (`MESSAGE.END` boundary), carrying that turn's byte accounting.
- `session_tool_call_unexecutable` — one per provider tool-call event that the
  session runtime cannot execute (the runtime has no tool executor).

## Stable field reference

Field maps carry string values keyed by these stable names. No human prose is
included in any field.

### `session_failure` fields

| Field | Meaning | Observed values in this suite |
| --- | --- | --- |
| `classification` | Public gateway taxonomy classification already produced on the typed stream error/close value (`go-llm-gateway/pkg/providers`). | `authentication`, `transport`, `invalid_request` |
| `terminal_reason` | Why the session terminated (`messages.TerminalReason`). | `terminal_failure`, `provider_close` |
| `terminal_provenance` | Layer that authored the terminal state (`messages.TerminalProvenance`). | `provider`, `gateway`, `session`, `cli` |
| `output_state` | Whether output was delivered before termination (`messages.TerminalOutputState`). | `none`, `partial` |
| `provider` | Lowercased provider name from the runtime plan. | `grok` |
| `model` | Model identifier from the capture/config. | `grok-4-failure-auth`, `grok-4-failure-disconnect`, `grok-4-failure-malformed`, `grok-4-dead-zeroturn`, `scripted-realtime` |
| `turns_completed` | Count of completed turns at termination. | `0`, `1` |
| `failing_event` | Stream-event identity that authored the failure. | `ERROR`, `SESSION.CLOSE`, `SESSION.CONNECT` (dial-phase), `SESSION.RUN` (run/drain phase without a captured stream event) |
| `provider_error_type` | Provider error type when the provider supplied one. Absent otherwise. | `invalid_api_key` |
| `provider_error_code` | Provider error code when present. Absent otherwise. | `invalid_api_key` |

### `session_turn_completed` fields

| Field | Meaning |
| --- | --- |
| `turn_index` | 1-based completed-turn position. |
| `input_audio_bytes` | User/microphone PCM bytes attributed to this turn. `0` means silent or truncated input. |
| `input_text_bytes` | User prompt text bytes attributed to this turn. |
| `output_audio_bytes` | Assistant audio bytes delivered during this turn. |
| `output_text_bytes` | Assistant text/transcript bytes delivered during this turn. |

### `session_tool_call_unexecutable` fields

| Field | Meaning | Observed values |
| --- | --- | --- |
| `tool_name` | Name the provider asked to call. | `get_weather` |
| `tool_call_id` | Provider call identifier. | `call_weather_001` |
| `failure_classification` | Gateway taxonomy class for an unexecutable request. | `unsupported_request` |
| `failure_reason` | Stable machine reason. | `no_tool_executor_in_session_runtime` |
| `turn_index` | Turn position (in-flight turn at the call). | `1` |

## Metrics series semantics

The recorder keeps four `(direction, modality)` series; each observation adds
one count and the payload's byte size:

| Responder vocabulary | Series key | Direction / Modality constants |
| --- | --- | --- |
| Microphone/user audio sent upstream | `input/audio` | `metrics.DirectionInput`, `metrics.ModalityAudio` |
| User prompt text sent upstream | `input/text` | `metrics.DirectionInput`, `metrics.ModalityText` |
| Assistant audio received downstream | `output/audio` | `metrics.DirectionOutput`, `metrics.ModalityAudio` |
| Assistant text/transcript received downstream | `output/text` | `metrics.DirectionOutput`, `metrics.ModalityText` |

Direction and modality semantics are the existing `pkg/metrics` ones, reused
unchanged. A healthy replayed multi-turn session shows nonzero
`output/audio` (`event_count=1, total_bytes=6`) and `output/text`
(`event_count=2, total_bytes=28`) series; a run that never carried media shows
zero-series everywhere (for example the auth-rejection run below).

## First-look fields per failure mode

Look at fields in the listed order; the first mismatch with expectations rules
the mode out. Values shown are the observed suite values.

### 1. Auth failure (`session_failure_auth.session.json`)

1. `classification` = `authentication`
2. `provider_error_type` = `invalid_api_key` (and identical
   `provider_error_code`)
3. `failing_event` = `ERROR`
4. `turns_completed` = `0` with `output_state` = `none`

Full observed record: `{"classification":"authentication","failing_event":"ERROR","model":"grok-4-failure-auth","output_state":"none","provider":"grok","provider_error_code":"invalid_api_key","provider_error_type":"invalid_api_key","terminal_provenance":"provider","terminal_reason":"terminal_failure","turns_completed":"0"}`.
All four metric series are zero: no media flowed before rejection.

### 2. Mid-session disconnect (`session_failure_disconnect.session.json`)

1. `classification` = `transport`
2. `terminal_reason` = `provider_close` with `terminal_provenance` = `session`
3. `output_state` = `partial` and `turns_completed` = `1` (at least one turn had
   completed)
4. `failing_event` = `SESSION.CLOSE`
5. `output/text` series nonzero: `event_count=1, total_bytes=40` — the partial
   answer delivered before the transport died.

Full observed record: `{"classification":"transport","failing_event":"SESSION.CLOSE","model":"grok-4-failure-disconnect","output_state":"partial","provider":"grok","terminal_provenance":"session","terminal_reason":"provider_close","turns_completed":"1"}`.

### 3. Malformed provider response (`session_failure_malformed_frame.session.json`)

1. `classification` = `invalid_request` with `terminal_provenance` = `gateway`
   (the gateway parser authored the failure, not the provider)
2. `provider_error_type` and `provider_error_code` are ABSENT — the frame never
   parsed far enough to carry provider identity
3. `failing_event` = `ERROR`, `turns_completed` = `0`, `output_state` = `none`

Full observed record: `{"classification":"invalid_request","failing_event":"ERROR","model":"grok-4-failure-malformed","output_state":"none","provider":"grok","terminal_provenance":"gateway","terminal_reason":"terminal_failure","turns_completed":"0"}`.

### 4. Tool-call failure (`session_failure_tool_call.session.json`)

No `session_failure` record is emitted (the capture closes cleanly); instead:

1. Exactly one `session_tool_call_unexecutable` record naming the called tool:
   `tool_name` = `get_weather`, `tool_call_id` = `call_weather_001`
2. `failure_classification` = `unsupported_request`,
   `failure_reason` = `no_tool_executor_in_session_runtime`
3. `turn_index` = `1`

### 5. Truncated/silent audio input (programmatic via `SendAudioInput`)

No terminal failure occurs; diagnosis comes from per-turn accounting:

1. Turn record `turn_index=1` has `input_audio_bytes` = `"0"` while a later
   voiced turn reports nonzero input (`turn_index=2` observed with
   `input_audio_bytes="4"`) — the silent input is attributed to its own turn.
2. Corroboration in the snapshot: `input/audio` series =
   `event_count=1, total_bytes=4` (only the voiced injection ever counted).

## Worked example: tracing one fixture to its artifacts

Committed fixture `go-llm-gateway/pkg/testing/testdata/session-fixtures/session_failure_disconnect.session.json`
replays, over the WebSocket-level transport, a session that opens, streams one
partial text answer, then ends without a session close (`ends_with_disconnect:
true`):

1. `session.update` (client→server), `session.created` (server→client),
   `response.created`, `response.text.delta` with
   `"partial answer before the transport died"`, then `response.done` and a raw
   transport death.
2. Running the real pipeline (`RunSession` over
   `gwtesting.NewReplayWebSocketDialer`) emits first a turn record
   `{event: "session_turn_completed", turn_index: "1", output_text_bytes: "40",
   input_audio_bytes: "0", input_text_bytes: "0", output_audio_bytes: "0"}`
   (40 = length of the streamed answer), then exactly one canonical failure
   record `{"classification":"transport","failing_event":"SESSION.CLOSE","model":"grok-4-failure-disconnect","output_state":"partial","provider":"grok","terminal_provenance":"session","terminal_reason":"provider_close","turns_completed":"1"}`,
   and finishes with `output/text` = `(event_count=1, total_bytes=40)` and all
   other series zero.
3. A responder reading only those artifacts identifies mode
   `mid_session_disconnect` at `turns_completed=1`: transport-classified close,
   session-provenance, partial output after one completed turn.

## Negative control: the dead session

Capture `session_dead_zeroturn.session.json` opens and immediately disconnects,
producing zero turns. Its real artifacts contain one failure record
`{"classification":"transport","failing_event":"SESSION.CLOSE","model":"grok-4-dead-zeroturn","output_state":"none","provider":"grok","terminal_provenance":"session","terminal_reason":"provider_close","turns_completed":"0"}`
and all-zero series. This does NOT satisfy any closed-set diagnosis: it lacks
the auth signature (wrong classification, missing provider error identity), the
disconnect signature (`output_state` is `none`, not `partial`;
`turns_completed` is `0`, not `1`; zero `output/text` series), the malformed
signature (wrong provenance), the tool-call signature (no tool-call record), and
the silent-input signature (no turn records at all). The suite asserts each of
these mismatches so the empty run can never pass as diagnosed.
