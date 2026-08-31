# Room Recording Bundle Layout

---
author: Codex
owner: Agent CLI maintainers
last modified: 2026, August, 30
---

`agent room run --out <dir>` writes a complete evidence bundle for the
conversation into `<dir>`. This document is the authoritative reference for
that bundle's on-disk layout, so downstream test authors (deterministic room
record/replay, the audio-property assertion suite) can rely on stable paths
and field names instead of re-deriving them.

Recording is enabled by default; a manifest can disable it with
`room.recording.enabled: false`, in which case none of these artifacts are
written. Everything below is additive to the bundle that existed before this
document: prior artifacts (`run-manifest.json`, `agent-<id>.wav`,
`agent-<id>.diagnostics.jsonl`, `agent-<id>.deltas.jsonl`) keep their exact
names and shapes, only gaining the wall-clock fields described in
"Wall-clock timestamps" below.

## Degraded recording

Room recording follows the shared transcript `recording_status` contract. A
healthy bundle omits the optional status; if any evidence sink fails, the
room and the affected participant manifest carry `{"state":"partial",
"reason":"..."}` and `degraded_artifacts` maps each affected relative path
to its sanitized first failure. The room's runtime result and participant
termination reasons remain independent of this status: recording failures do
not cancel a conversation, retire a participant, or turn a clean room stop
into a runtime error. The first failure is retained so later writes to the
same or another degraded sink cannot replace the diagnostic cause.

## Directory layout

```text
<dir>/
  run-manifest.json                      # bundle manifest (see below)
  room-timeline.jsonl                    # room-level ordered event log
  room-mix.wav                           # composite "fly on the wall" mix
  agent-<id>.wav                         # this participant's own output (unchanged)
  agent-<id>.diagnostics.jsonl           # session diagnostics (now wall-clock stamped)
  agent-<id>.deltas.jsonl                # raw provider stream deltas (now wall-clock stamped)
  participants/
    <id>/
      sent.pcm                           # raw PCM16: what this participant spoke into the room
      received.pcm                       # raw PCM16: what the room delivered to this participant
```

`<id>` is a filesystem-safe stem derived from the manifest participant ID
(see `normalizeRoomArtifactStem` in `agent-cli/internal/services`); a
collision between two IDs that normalize to the same stem gets an appended
hash suffix. `run-manifest.json` is authoritative for every artifact's exact
relative path — look up `participants[<id>].artifacts` and the top-level
`room_mix`/`room_timeline` fields rather than reconstructing paths.

## `sent.pcm` and `received.pcm`

Earlier bundles recorded only `agent-<id>.wav`: each participant's own
output, and nothing about what it received. That meant a bundle alone could
never confirm room mixing/fan-out worked, or that a participant actually
heard what another participant said.

- `participants/<id>/sent.pcm` is raw, headerless PCM16 (little-endian,
  mono) — byte-identical to the audio also written to `agent-<id>.wav`, but
  without a WAV header, for callers that want to concatenate or diff raw
  samples directly. It covers both agent participants (their own assistant
  audio output) and human participants (their captured microphone input).
- `participants/<id>/received.pcm` is raw PCM16 of the room's mixed inbound
  stream actually delivered to that participant: for an agent participant,
  exactly the frames handed to `SendAudioInput`; for a human participant,
  exactly the frames written to their output device. It is written
  frame-by-frame as the room runs, independent of whether the participant's
  own session accepted or used that audio, so it reflects the room's ground
  truth even when delivery downstream fails.
- Sample rate and channel count for both files are named once, in
  `run-manifest.json`'s top-level `audio_format` object
  (`{"sample_rate": 24000, "channels": 1, "encoding": "pcm_s16le"}`), rather
  than being implicit or requiring a WAV header to discover.

## Wall-clock timestamps

Every JSON object written to `agent-<id>.diagnostics.jsonl` and
`agent-<id>.deltas.jsonl` (and every `room-timeline.jsonl` entry, see below)
now carries two additional top-level fields:

- `t_offset_ms` (number, fractional milliseconds): elapsed time since the
  room's real start, i.e. since `run-manifest.json`'s `timing.clock_base`.
- `t_unix_ms` (integer): the same instant as a Unix millisecond timestamp.

These are additive fields; existing readers that decode into a typed
`StreamMessage` envelope or a `{"event", "fields"}` diagnostic record are
unaffected because unrecognized JSON fields are ignored by `encoding/json`
decoding into a defined struct.

`run-manifest.json`'s `timing` object also gains `clock_base`:

```json
"timing": {
  "started_at": "2026-08-30T20:14:03.512Z",
  "ended_at":   "2026-08-30T20:14:41.098Z",
  "elapsed":    "37.586s",
  "clock_base": "2026-08-30T20:14:03.512Z"
}
```

`clock_base` is the same instant as `started_at`, named explicitly so a
downstream latency computation (`t_unix_ms - clock_base`) does not have to
guess which timing field is the anchor. It is always the room's real start
time — never an epoch placeholder — so per-turn and cross-participant
latency (e.g. barge-in response time) is directly computable from the
bundle.

(This clock is unrelated to `agent session --record-dir`'s existing,
deliberately-fixed 1970-01-01 `clock_base`: that single/dual-participant
session recording path uses a fixed logical clock on purpose, to keep
otherwise-identical recordings byte-comparable across runs. Rooms have no
such byte-comparability requirement across live runs, so their clock is
simply real wall-clock time.)

## `room-timeline.jsonl`

The room-level companion to the per-participant streams: one ordered JSONL
log of the whole conversation's shape, independent of any single
participant's own file. Each line is:

```json
{"t_offset_ms": 1234.5, "t_unix_ms": 1700000001234, "event": "speech_start", "participant": "alice", "fields": {}}
```

`fields` is omitted when empty. Recognized `event` values:

| Event | `participant` | Meaning |
|---|---|---|
| `participant_joined` | joining participant | mesh join completed |
| `participant_ready` | ready participant | provider session ready to converse |
| `participant_failed` | failed participant | a participant-local fault retired the participant; `fields.reason` contains the sanitized cause |
| `participant_liveness_fault` | affected participant | a positively classified provider liveness failure; `fields.reason` is the stable classification and the same event is broadcast to every current room-stream subscriber |
| `participant_terminated` | terminated participant | `fields.reason` names the termination reason |
| `response_start` | speaker | provider `MESSAGE.START`; `fields.response_id` |
| `response_end` | speaker | provider `MESSAGE.END`; `fields.response_id`, `fields.terminal_reason`, `fields.output_state` |
| `speech_start` / `speech_end` | speaker | this participant's own audio output started/stopped (energy-based; `speech_end` also fires on an explicit `AUDIO.END`) |
| `received_speech_start` / `received_speech_end` | listener | the room's mixed stream delivered to this participant transitioned into/out of audible signal — the observable counterpart of `speech_start` on the other side |
| `barge_in_cancel_acked` | interrupted speaker | the provider reported its response as cancelled (`terminal_reason=cancellation`) — a successful barge-in |
| `barge_in_cancel_failed` | interrupted speaker | the provider rejected a cancel request (`response_cancel_not_active`); `fields.code`, `fields.message` |
| `audio_input_dropped` | listener | non-silent incoming audio was not delivered to this participant's session; `fields.reason`, `fields.bytes` (see below) |
| `provider_error` | affected participant | any other provider `ERROR` event; `fields.code`, `fields.classification`, `fields.message` |
| `tool_call_start` / `tool_call_end` | caller | `fields.tool_call_id` |
| `turn_completed` | speaker | `fields.turn_index` |
| `run_terminated` | (room-level, no participant) | `fields.reason` |

Because `speech_start`/`speech_end` (what a participant said) and
`received_speech_start`/`received_speech_end` (what another participant
heard) share one ordered, wall-clock-stamped log, overlapping or
simultaneous speech across participants is directly visible by comparing
offsets — this is what makes a conversation's shape (including barge-in and
double-talk) machine readable without reconstructing it from N separate
per-participant files.

## Dropped/ignored incoming audio is now diagnosable

Before this bundle layout, a participant that never received (or never
acted on) another participant's speech showed up only as a silent
`input_audio_bytes: 0` in its own diagnostics — indistinguishable from the
ordinary case of nobody speaking. Two things now make that defect
observable directly from the bundle instead of requiring separate
instrumentation:

1. `participants/<id>/received.pcm` is ground truth for what the room
   delivered to that participant, regardless of whether its session
   accepted or used it — silence there when another participant was
   speaking (checkable against that other participant's `sent.pcm` or
   `speech_start`/`speech_end` timeline entries) is now directly visible.
2. If the room detects real (non-silent) incoming audio that it fails to
   hand off to that participant's session, it writes an explicit
   `room.audio.input_dropped` diagnostic record to that participant's
   `agent-<id>.diagnostics.jsonl` and an `audio_input_dropped` entry to
   `room-timeline.jsonl`, both carrying the failure reason and byte count,
   instead of leaving the failure silent.

## `room-mix.wav`

The composite "fly on the wall" mix of every participant's own spoken
audio (`sent.pcm`/`agent-<id>.wav`) on one shared timeline: each
participant's audio is summed into the mix at the real wall-clock offset it
was produced, so simultaneous speech from multiple participants is audible
together, the way a listener physically present in the room would hear it.
It is a standard mono 16-bit PCM WAV file at the room's configured sample
rate (the same rate named in `audio_format`).

`room-mix.wav`'s duration always matches the room's real wall-clock span
(`timing.ended_at - timing.started_at`): the composite buffer is padded with
silence out to that span at finalization, so a shorter tail of true silence
after the last participant stopped speaking does not shrink the file, and a
reader can evaluate the whole conversation, including trailing dead air,
against one fixed-length recording.

## Regressions

`agent-cli/internal/services/session_room_recording_completeness_test.go`
runs a hermetic (scripted-provider, no network) two-participant room and
asserts: both `sent.pcm`/`received.pcm` exist and `received.pcm` is
non-empty and non-silent when the other participant spoke; every recorded
delta and diagnostic event carries `t_offset_ms`/`t_unix_ms`;
`manifest.timing.clock_base` is a real, non-epoch timestamp;
`room-timeline.jsonl` is chronologically ordered and orders the scripted
overlapping speech correctly; and `room-mix.wav`'s sample count matches the
room's wall-clock span. `session_room_evidence_capture_test.go` unit-tests
the underlying primitives (wall-clock injection, the speech-segment
tracker, the mix buffer's overlap summation and span padding, and the
dropped-audio diagnostic) directly.
