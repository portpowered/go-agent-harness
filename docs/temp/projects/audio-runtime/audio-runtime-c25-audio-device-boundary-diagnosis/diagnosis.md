# C25 audio/device boundary diagnosis

- Task: `audio-runtime-c25-audio-device-boundary-diagnosis`
- Candidate revision: `70f2496f16b8bf036cbe654d99a121ac0a1346f7`
- Source revision: `a1156f0c0c6271643578cb37894026944df2a633`
- Source archive SHA-256: `0e8f182d3c58853213cf575e2d7b6e0a125b92fab96ac3c67969b8ab409c841f`
- Decision basis: source-only diagnosis; no realtime, provider, physical-device, or acoustic claim.
- The pinned source is required to be an ancestor of the candidate and every audited production path is required to be unchanged from that source.

## Finding

`SOURCE_GAP_CONFIRMED`: the smallest remaining bypass is the compiled legacy CLI room path in `agent-cli/internal/room/mixer.go` and its `agent-cli/internal/services/internal/agentruntime` callers.

That path owns a host `time.Ticker`, local PCM16 encode/decode, direct device construction, and direct sink writes. The current public `yui room run` command is wired to `go-agent-runtime/services/rooms`, so this packet does not claim that the legacy path is the active public workflow. It does establish a concrete production-source ownership gap and migration hazard: the old alternate implementation remains compiled and exercised by its own internal package tests while duplicating the intended audio/device boundaries; `servicetest/runtime.go` imports that package for session helpers but exports no `RunRoom` API.

## Focused causal evidence

- Legacy lifecycle mixer field: `[74]` (the actual field is at `session_room_lifecycle.go:74`).
- Service-test import: `[9]`; `RunRoom` exports: `[]`.

- `legacy-host-cadence` — `agent-cli/internal/room/mixer.go`: The legacy room mixer owns cadence with time.Ticker instead of an injected audio/pkg/clock scheduler.
  - `time_import` lines: 11
  - `new_ticker` lines: 167
  - `pcm_mixer_type` lines: 224
- `legacy-local-pcm-codec` — `agent-cli/internal/room/mixer.go`: The legacy mixer decodes and encodes raw PCM16 inside room orchestration rather than passing canonical PCMFrame values through the audio subsystem.
  - `codec_import` lines: 13
  - `decode` lines: 736
  - `encode` lines: 760
- `legacy-direct-device-construction` — `agent-cli/internal/services/internal/agentruntime/session_room_orchestration.go`: The legacy room orchestration creates both the local PCM mixer and physical device endpoints in the same implementation.
  - `device_import` lines: 3
  - `mixer_constructor` lines: 161
  - `source_constructor` lines: 494
  - `sink_constructor` lines: 499
- `legacy-direct-device-write` — `agent-cli/internal/services/internal/agentruntime/session_room_run.go`: The legacy human output loop resamples/decodes pending PCM and writes device frames directly, so queue admission and actual consumption are not represented by the canonical device runtime boundary.
  - `input_read` lines: 1049
  - `resample` lines: 1057, 1074, 1180
  - `decode` lines: 1176
  - `sink_write` lines: 1186

## Dependency controls

- Positive canonical graph: `ACCEPTED`.
- Negative bypass graph: `REJECTED_AS_FORBIDDEN`.
- Fixture manifest: `ACCEPTED` with exact SHA-256/byte/line checks for both owned fixtures.
- Pinned source archive: `ACCEPTED`; rebuilt Git archive SHA-256 is `0e8f182d3c58853213cf575e2d7b6e0a125b92fab96ac3c67969b8ab409c841f`.
- Changed-path allowlist: `ACCEPTED` for the complete source-to-candidate and working-tree path set.
- The canonical `go-audio/pkg/mixer` consumes an injected `clock.TimerSource`, submits `audio.PCMFrame` values into bounded frame buffers, and exposes media ports; device runtime owns the playback queue and callback boundary.

## One extraction plan

1. Keep the public room service as the only room-run entrypoint; assign the legacy `agent-runtime` package owner to migrate or delete `RunRoom` and its `PCM16Mixer` callers, with no edits to C18/C21-owned files without their primary task.
2. Replace `room.PCM16Mixer` with the canonical `go-audio/pkg/mixer` and `audio.PCMFrame`/buffer contracts; inject `clock.TimerSource` instead of constructing `time.Ticker` in room code.
3. Move human capture/output adaptation behind the runtime device service (`RTCDeviceSource`/`RTCDeviceSink` and `MediaPorts`), preserving an explicit distinction between queued frames and callback-consumed samples.
4. Add one external public-consumer regression that proves frame ordering, end-of-response/epoch boundaries, cancellation/close, and partial-vs-full device consumption; add a dependency guard that rejects host ticker, local codec, and direct gateway imports in room orchestration.

## Limits

- C15/C16/C17 accepted probes establish software replay, software sink lifecycle, and canonical PONG-clock behavior only; they do not establish physical playback, microphone capture, live provider behavior, or acoustics.
- This packet deliberately changes no production/shared module, fixture, or baseline path.
