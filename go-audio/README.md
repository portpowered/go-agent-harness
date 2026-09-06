# Shared audio subsystem

This module owns PCM/base64 and Opus payload codecs, WAV parsing and writing,
continuous resampling, framing, signal processing, media buffers, clocks,
and audio recording/replay. It has no dependency on the CLI, agent loop,
provider gateway, or device gateway.

- `pkg/audio`: PCM formats/frames, bounded directional buffers, stream and
  playback processors, route workers, feedback detection, media endpoints,
  playback commands/receipts, analysis, sources and sinks.
- `pkg/codec`: PCM16 and Opus payload encoding/decoding. Provider adapters own
  their JSON/RTP envelopes; they use this package for audio payloads.
- `pkg/wavio`: canonical WAV container validation, streaming and whole-file
  I/O, and continuous resampling.
- `pkg/clock`: real and deterministic clock/scheduler implementations.
- `pkg/recording`: bounded asynchronous boundary recording and integrity-checked
  media replay on a virtual clock. This is not a simulator of a remote model.
- `pkg/observability`: shared observation contracts used by audio/device owners.

`FrameProducer` and `FrameConsumer` are concrete memory capabilities. They
cannot call a device, network, disk writer or caller-supplied callback. An
external runtime owns I/O workers and their lifetime. `RouteEngine.Run` runs
independently of agent ticks. Epoch invalidation rejects stale queued frames.

Normal response completion preserves the exact tail. Interruption discards
pending samples explicitly. Device consumption and software queue admission
are different counters. Playback commands have a separate bounded lane and
an applied receipt; a receipt does not establish what a physical listener heard.

Run independently from this directory:

```sh
GOWORK=off go test ./...
GOWORK=off go test -race ./...
```

The design and migration acceptance record is in
`../docs/architecture/audio-subsystem-device-gateway-design.md`.
