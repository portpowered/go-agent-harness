# Audio runtime C51: audio/device boundary characterization

This is an evidence-only characterization for the admitted task
`audio-runtime-c51-audio-device-boundary-characterization`. The production
source is pinned to the fetched integrated main `bb29005d0bb545db5e08ffda0205929b021d4fc1`; the startup
integration `8bdafc7f947a3a2c9856220abdc539437035bd21`, planning main
`7f73c8b3b4ebc99b55b8bb5e802beff024385407`, and baseline ancestry are recorded
in `provenance.json`. No production, shared
fixture, baseline, acceptance, or sibling-owner file is changed.

`review-findings.json` records the exact canonical-board lookup and the
concluded C51 review rows `work-review-27` and `work-review-35` for PR #441.
Their rejections are actionable evidence repairs, not a CI waiver: citations,
provenance, shipped-build binding, embedded inputs, live quotas, and runner
cleanup must all be corrected before resubmission.
`progress.txt` has no earlier C51 checkpoint; the board rejection is the
authoritative prior review inbox.

## Boundary result

The public embedded route is:

```text
devices.Request
  -> services/devices/wire.NewService(registry)
  -> devices.Service.Open
  -> devices.Handle.Media().Playback
  -> devices.Playback.Pump(ctx, audio.InboundMedia)
  -> RTCDeviceSink
  -> DeviceSink
  -> PlaybackQueue.Enqueue
  -> SimulatedDuplexRegistry.Advance
  -> PlaybackQueue.RenderInto
```

`boundary-map.json` names the owner, consumers, exact production callers and
their enclosing functions, timing domain, citations, evidence strength, and evidence disposition for
packet parsing, format negotiation, clocks/timing, DSP and resampling, bounded
buffers, the core-loop boundary, device lifecycle, the runtime adapter, and
trace/replay. The verifier checks every citation against the pinned source
line and rejects comments, fields, declarations used as calls, or unrelated
line-range hits; `boundary-negative-controls` proves those two caller failures
are rejected.

The consumption labels are deliberately separate:

| Label | C51 disposition |
| --- | --- |
| `QUEUE_ADMISSION` | `OBSERVED`: 480 samples are queued before `Advance`. |
| `BUFFER_RECEIPT` | `MAPPED`: runtime receipt follows the device queue write. |
| `FILE_OR_SOFTWARE_RECEIPT` | `NOT_USED`: no file sink is relabeled as device output. |
| `SOFTWARE_DEVICE_CALLBACK_CONSUMPTION` | `OBSERVED`: `Advance` renders exact PCM and records queue before/after. |
| `PHYSICAL_HARDWARE_CONSUMPTION` | `OUT_OF_SCOPE`: excluded by the admitted Windows-hardware/acoustic amendment. |

The external consumer in `consumer/` imports the public runtime service/wire,
audio, and device-gateway packages. It runs with `GOWORK=off`, records exact
frame metadata/order and timestamps-as-sample-cursors, checks idempotent
close, and proves that a queued frame is not yet callback-consumed. The
consumer's deliberate wrong oracle must fail with the marker recorded in
`evidence/wrong-consumption-oracle.json`.

## Reproduction

Run from the repository root with the admitted factory root exported:

```sh
export FACTORY_ROOT=/Users/abdifamily/.codex/worktrees/af44/go-agent-harness
python3 docs/temp/projects/audio-runtime/audio-runtime-c51-audio-device-boundary-characterization/verify.py --mode generate
python3 docs/temp/projects/audio-runtime/audio-runtime-c51-audio-device-boundary-characterization/verify.py --mode imports
python3 docs/temp/projects/audio-runtime/audio-runtime-c51-audio-device-boundary-characterization/verify.py --mode boundaries
python3 docs/temp/projects/audio-runtime/audio-runtime-c51-audio-device-boundary-characterization/verify.py --mode boundary-negative-controls
python3 docs/temp/projects/audio-runtime/audio-runtime-c51-audio-device-boundary-characterization/verify.py --mode consumption-levels
python3 docs/temp/projects/audio-runtime/audio-runtime-c51-audio-device-boundary-characterization/verify.py --mode consumer
python3 docs/temp/projects/audio-runtime/audio-runtime-c51-audio-device-boundary-characterization/verify.py --mode wrong-consumption-oracle
python3 docs/temp/projects/audio-runtime/audio-runtime-c51-audio-device-boundary-characterization/verify.py --mode runner-negative-controls
python3 docs/temp/projects/audio-runtime/audio-runtime-c51-audio-device-boundary-characterization/verify.py --mode repair-candidates
```

The consumer mode runs both normal and `-race` tests. Its dependency listing is
`GOWORK=off go list -deps ./...`; it must not contain `agent-cli`.

Build the shipped binary from the exact source before the process check:

```sh
export C51_YUI=/private/tmp/audio-runtime-c51-yui-exact-head
(cd agent-cli && GOWORK=off go build -trimpath -o "$C51_YUI" ./cmd/yui)
python3 docs/temp/projects/audio-runtime/audio-runtime-c51-audio-device-boundary-characterization/verify.py --mode write-build-manifest --binary "$C51_YUI"
python3 docs/temp/projects/audio-runtime/audio-runtime-c51-audio-device-boundary-characterization/verify.py --mode shipped-regression --binary "$C51_YUI" --build-manifest docs/temp/projects/audio-runtime/audio-runtime-c51-audio-device-boundary-characterization/evidence/shipped-build.json --child-timeout 60 --total-timeout 600
```

The shipped-build manifest hashes every tracked file under the local yui
workspace roots, including transitive Go `embed` inputs and local JS/JSON
assets such as the siteadapter extensions and `goal_catalog.json`; it compares
the tested revision with the evidence-only candidate head. The shipped
regression runs the committed healthy session replay through the
real `yui session --replay ... --audio-out ...` process with no credentials or
network. The exact WAV oracle is mono 16 kHz PCM16, 2 frames / 4 PCM bytes,
PCM SHA-256
`df3f619804a92fdb4057192dc43dd748ea778adc52bc498ce80524c014b81119`, and
WAV SHA-256
`3e9bdf4cffd0b09b1ce550cf199542cd54fdaf69bf4f53d2e1b8be88c02f5aa3`. It also
runs the committed tool-call fixture as a controlled unresolved-tool negative
control and requires the exact `call_weather_001` lifecycle error. Every child
is bounded by a live disk monitor and per-file WAV quota, and the runner
controls cover timeout, output, disk overflow, parent-exit descendants,
reader-start failure, aggregate deadline, and process-group reap status.

`provenance.json` is the final clean-tree gate: it records and rechecks the
project-control admission result, Go toolchain/GOOS/GOARCH, clean candidate
SHA, fetched `origin/main`, required startup/planning ancestry, source/archive
and authority hashes, all import-analysis input hashes, the verifier hash,
the boundary-map hash and exact shipped build-input manifest. The shipped regression is
classified exactly `SOFTWARE_REPLAY_PROCESS_ONLY`; its binary is accepted only
when the manifest proves its hash, Go inputs, toolchain, fixtures, and clean
tested revision. Child output, WAV output, temporary owned disk growth,
aggregate deadlines, and descendant process cleanup are bounded and recorded.
Native Windows endpoints and physical/acoustic proof remain `OUT_OF_SCOPE`; no
project-wide acceptance or CI result is claimed here.
