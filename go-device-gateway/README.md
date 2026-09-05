# Device gateway

This module owns device discovery, selection, native backends, simulated devices,
and the workers that move audio between device handles and memory buffers.
It depends on `go-audio`; it does not depend on the CLI, agent loop, or provider
SDKs.

- `pkg/devices` exposes device registry, source/sink and playback accounting
  contracts. Platform backends and virtual device implementations live here.
- `pkg/runtime` owns capture and playback workers, feedback integration,
  cancellation, queued playback controls and the device-facing clock boundary.
  Codecs, resampling, framing, buffers and DSP come from `go-audio`.

Create `BufferedCapture` before starting the session loop. Its producer,
consumer and control capabilities refer to the actual capture handoff used by
`PumpBufferedCaptureWithBuffer`. The provider writer runs independently of the
capture worker. Uploaded-audio observation occurs after the provider endpoint
accepts its write.

Playback control has a separate bounded queue from PCM. Worker receipts carry
command identity and epoch; stale commands are rejected. Playback snapshots
expose queued and consumed sample counts without granting the loop a device
handle. The runtime owner closes sinks and sources and joins their workers;
cancellation of a buffer consumer alone does not close a native device.

The agent loop integrates memory ports through its audio subsystem. Application
services own selecting and opening devices and injecting those ports. CLI
commands consume the application services through generated Wire composition.

```sh
GOWORK=off go test ./...
GOWORK=off go test -race ./...
```

Native hardware verification is opt-in through the private runtime test
`TestRTCDeviceBindingHardwareRoundTrip` in
`agent-cli/internal/services/internal/agentruntime`, with
`AGENT_HARNESS_RTC_HARDWARE=1`.
