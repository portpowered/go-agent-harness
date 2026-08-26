# s2s vertical v9 — WebRTC device round trip

Status: **proven in-repo with deterministic device bindings** (2026-08-26).
The hardware acceptance command is wired through the public probe route and
records a structured skip on a headless host.

## Scenario and command surface

The committed scenario is
`agent-cli/internal/cli/testdata/probe-scenarios/s2s-v9-webrtc-device-roundtrip.scenario.json`.
The T2 device-tier entry point is:

```sh
agent probe run \
  agent-cli/internal/cli/testdata/probe-scenarios/s2s-v9-webrtc-device-roundtrip.scenario.json \
  --devices real --json
```

`--devices real` enumerates the shared `audio.DeviceRegistry` before any device
is opened. If either direction is absent, the command emits one JSON result
per selected scenario with `status: "skip"`, a stable `reason_code`, and a
human-readable `reason`; the summary also reports `status: "skip"` and exits
successfully. `--out` and `--summary` can be used to record those JSONL
artifacts separately.

`TestS2SV9WebRTCDeviceProbeIsReachableThroughPublicCLI` executes this exact
route through the production Cobra router and asserts the no-device result.
The test uses the repository's intentionally headless default registry, so it
does not touch host hardware or provider credentials.

## WebRTC/session proof

`TestS2SV9WebRTCDeviceCaptureProvesRegistryToSession` covers the positive
device-round-trip shape with the existing virtual registry as a deterministic
CI fixture. It:

1. selects and opens the registry's input and output devices;
2. sends captured PCM through negotiated Pion Opus and reads it back through
   `rtc.InboundTrack`;
3. forwards the decoded frame and turn boundary through
   `participants.NewSessionModelRunner`;
4. delivers provider-style transcript and audio events through that same
   session boundary;
5. writes the response to the selected sink, reads the registry loopback, and
   asserts RMS strictly above `audio.DefaultVADConfig.EnergyThreshold`; and
6. asserts a non-empty transcript containing `device round trip`.

The virtual fixture is evidence for the registry, codec, session, sink, RMS,
and transcript contracts only. It is not presented as physical-device
coverage. A hardware acceptance host must run the T2 command with its real
registry/device-binding implementation; hosts without both directions take the
recorded SKIP path.

`TestDeviceProbeRuntimeUsesBoundDevicesAndSessionOutput` additionally invokes
the production `runDeviceProbeScenario` executor. Its hermetic session seam
stands in for the live provider while the executor still opens both selected
registry devices, negotiates both local WebRTC Opus links, forwards captured
audio through `participants.NewSessionModelRunner`, writes provider audio to
the selected sink, and returns the output/transcript observation consumed by
the command's normal probe evaluator. The command constructor uses the live
OpenAI/Grok session factory for the hardware path.

The named negative controls fail with diagnostics that include the observed
RMS and threshold for silent output, or the exact empty/mismatched transcript.
