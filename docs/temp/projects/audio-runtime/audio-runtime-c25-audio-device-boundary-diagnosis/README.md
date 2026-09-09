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

The verifier pins freshly fetched main revision `98ce636dd67349ba64f22cd7916dd370cf4ba484` separately from the docs candidate, requires that source to be an ancestor, verifies every audited production path is unchanged from the pin, enforces a 55-second child cap and 600-second aggregate cap, terminates timed-out process groups, records source SHA-256 values, checks the public/canonical package graph, rejects a negative bypass fixture, and runs only these focused checks:

- canonical `go-audio/pkg/mixer` mixer/accumulator tests;
- canonical playback queue/callback-boundary tests;
- canonical deterministic-clock boundary tests;
- canonical room lifecycle graph/media-bridge tests;
- the go-agent-loop production ownership guard;
- one legacy mixer behavior test;
- one legacy agent-runtime behavior test, its race run, and focused vet.

The pushed candidate-head run completed in 10.325 seconds; `diagnosis.json`, `diagnosis.md`, and `provenance.json` record exact file hashes, timestamps, native exits, and harness verdicts. Every child is bounded to 55 seconds and the aggregate to 600 seconds with process-group cleanup.

The exact current-head refresh at `209afd4ceca417fdca7964e3c0301ad4d3d1c250`
completed in 10.263 seconds with 19 commands, no unexpected failure, clean
source/path checks, and no surviving child processes. The generated
`diagnosis.json` and `provenance.json` are stamped with that candidate; the
earlier ff00c037, dab5c373, 3c2aea55, and 10.325-second runs remain preserved as
predecessor checkpoints.

After merging fetched `origin/main` at `98ce636dd67349ba64f22cd7916dd370cf4ba484`,
the exact candidate refresh at `88f996c5384222d97b92578a6ba7f1ea83adbf2e`
completed in 9.475 seconds with 19 bounded commands. It reported
`SOURCE_GAP_CONFIRMED`, accepted source ancestry and audited-path equality,
accepted every focused package/control check, and recorded the intentional
1-second child-hang control as an expected terminated failure with no surviving
children. The refreshed source archive is `e77933bdc0f47a2f885581eb9b51a297b763f7a7b54bffe3b07254fa2126f11e`.

The final exact-head refresh at pushed candidate
`672659326172756e26a228009560062d412c6650` completed in 9.680 seconds with
the same 19-command result and no unexpected failure. `diagnosis.json`,
`diagnosis.md`, and `provenance.json` identify this exact pushed HEAD.

The current-head CI rejection was separately characterized read-only. Run `34397773527` / job `102621627290` failed only in the hosted integration matrix at `test46/provider_burst`: after all provider responses and callback-clock shutdown, the full `/v1/audio-device/control/snapshot` evidence request exceeded the existing 30-second scenario context at `session_tool_audio_remote_e2e_test.go:182`. The production-binary device replay and committed regression suite in that job passed. The exact production/fixture owner is C20; C25 makes no repair outside this evidence folder. Local single and full-matrix focused runs passed, so this remains an intermittent hosted diagnosis rather than a claimed fix.

C20's reviewed recording/live repair has not reached `origin/main` in this refresh;
the timeout remains C20-owned historical evidence. C25 therefore imports no C20
production or fixture changes and makes no CI-green claim.

## Extraction plan

The single chosen dependency is the legacy room-owned PCM16 cadence/mixer boundary. The exact current/proposed paths, APIs, callers, ownership, wire sequence, trigger, executable regression, and AUDIO/DEVICE/SERVICE/QUALITY gate map are in [`extraction-plan.md`](extraction-plan.md) and [`extraction-plan.json`](extraction-plan.json).

Accepted C15/C16/C17 evidence proves software replay, software sink lifecycle, and canonical PONG-clock behavior only. Physical playback, microphone capture, live provider behavior, and acoustics remain unproved.
