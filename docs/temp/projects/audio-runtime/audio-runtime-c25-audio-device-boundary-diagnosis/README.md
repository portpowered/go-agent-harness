# C25 audio/device boundary diagnosis

This is the admitted `audio-runtime-c25-audio-device-boundary-diagnosis` evidence folder. It is intentionally source-only: no production, shared-module, fixture, baseline, device, provider, realtime, or acoustic changes are part of this task.

## Finding

The smallest concrete remaining bypass is the compiled legacy CLI room implementation:

- `agent-cli/internal/room/mixer.go` owns a host `time.Ticker` and local PCM16 encoding/decoding.
- `agent-cli/internal/services/internal/agentruntime/session_room_orchestration.go` constructs that mixer and opens `go-device-gateway` source/sink handles.
- `agent-cli/internal/services/internal/agentruntime/session_room_run.go` resamples/decodes pending PCM and calls `sink.WriteFrame` directly.

The current public `yui room run` command is wired through `go-agent-runtime/services/rooms` (`agent-cli/internal/wire/wire_gen.go:109-110` and `agent-cli/internal/transport/cli/room.go:52`), so this packet does not claim that the legacy path is the active public workflow. The old `RunRoom` path is nevertheless compiled (`go list` and compile-only `go test` pass) and exposed through the CLI service-test seam. That is a concrete source/ownership gap and migration hazard, not runtime/acoustic proof.

## Verification

Run from the repository root:

```text
python3 docs/temp/projects/audio-runtime/audio-runtime-c25-audio-device-boundary-diagnosis/verify.py --mode all
```

The verifier enforces a 55-second child cap and 600-second aggregate cap, terminates timed-out process groups, records source SHA-256 values, checks the public/canonical package graph, rejects a negative bypass fixture, and runs only these focused checks:

- canonical `go-audio/pkg/mixer` mixer/accumulator tests;
- the go-agent-loop production ownership guard;
- one legacy mixer behavior test;
- compile-only legacy `agentruntime` package check.

The last successful run at the admitted baseline completed in 3.552 seconds with clean shutdown. See `diagnosis.json`, `diagnosis.md`, and `provenance.json` for machine-readable output and exact file hashes.

## Extraction plan

1. Keep the public room service as the only room-run entrypoint and have the legacy package owner migrate or delete `RunRoom` and its `PCM16Mixer` callers; do not edit C18/C21-owned files without their primary task.
2. Replace `room.PCM16Mixer` with `go-audio/pkg/mixer`, `audio.PCMFrame`, and bounded buffer contracts, injecting `clock.TimerSource` instead of constructing `time.Ticker` in room code.
3. Move human capture/output adaptation behind `RTCDeviceSource`/`RTCDeviceSink` and `MediaPorts`, retaining the distinction between queue admission and callback consumption.
4. Add one external public-consumer regression for frame/epoch/end-of-response ordering, cancellation/close, and partial-vs-full device consumption, plus a dependency guard rejecting host ticker, local codec, and direct gateway imports from room orchestration.

Accepted C15/C16/C17 evidence proves software replay, software sink lifecycle, and canonical PONG-clock behavior only. Physical playback, microphone capture, live provider behavior, and acoustics remain unproved.
