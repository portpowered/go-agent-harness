# Audio runtime C51: audio/device boundary characterization

This is an evidence-only characterization for the admitted task
`audio-runtime-c51-audio-device-boundary-characterization`. The production
source is pinned to `7f73c8b3b4ebc99b55b8bb5e802beff024385407`; the startup
integration `8bdafc7f947a3a2c9856220abdc539437035bd21`, planning main, and
baseline ancestry are recorded in `provenance.json`. No production, shared
fixture, baseline, acceptance, or sibling-owner file is changed.

`review-findings.json` records the exact canonical-board lookup. The C51 task
has no concluded review row and no prior rejection feedback. `progress.txt`
has no C51 entry, so no finding is invented from predecessor history.

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

`boundary-map.json` names the owner, consumers, citations, and evidence
disposition for packet parsing, format negotiation, clocks/timing, DSP and
resampling, bounded buffers, the core-loop boundary, device lifecycle, the
runtime adapter, and trace/replay.

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
export C51_YUI=/tmp/audio-runtime-c51-yui-7f73c8b3
(cd agent-cli && GOWORK=off go build -trimpath -o "$C51_YUI" ./cmd/yui)
python3 docs/temp/projects/audio-runtime/audio-runtime-c51-audio-device-boundary-characterization/verify.py --mode shipped-regression --binary "$C51_YUI" --child-timeout 60 --total-timeout 600
```

The shipped regression runs the committed healthy session replay through the
real `yui session --replay ... --audio-out ...` process with no credentials or
network. The exact WAV oracle is mono 16 kHz PCM16, 2 frames / 4 PCM bytes,
PCM SHA-256
`df3f619804a92fdb4057192dc43dd748ea778adc52bc498ce80524c014b81119`, and
WAV SHA-256
`3e9bdf4cffd0b09b1ce550cf199542cd54fdaf69bf4f53d2e1b8be88c02f5aa3`. It also
runs the committed tool-call fixture as a controlled unresolved-tool negative
control and requires the exact `call_weather_001` lifecycle error. Every child
is bounded, and the report records return code, output limits, and process-group
reap status.

`provenance.json` is the final clean-tree gate: it checks branch/PRD identity,
origin/main pin and required ancestry, source/archive and authority hashes,
all import-analysis input hashes, the script hash, owned-path-only changes, and
the 2 GiB compile reserve. Native Windows endpoints and physical/acoustic proof
remain `OUT_OF_SCOPE`; no project-wide acceptance or CI result is claimed here.
