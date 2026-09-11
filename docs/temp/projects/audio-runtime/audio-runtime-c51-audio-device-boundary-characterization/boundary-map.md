# C51 audio/device boundary map

The characterized source is `7f73c8b3b4ebc99b55b8bb5e802beff024385407`, the
isolated branch's planning-main checkpoint. `import-graph.json` is generated
from production Go imports with file/line citations and checked with
`GOWORK=off go list -json ./...` for every workspace module. The map names the
first owner of each signal and the downstream consumer; it does not infer
physical playback from a queue write.

The public embedded seam is:

```text
devices.Request
  -> services/devices/wire.NewService(registry)
  -> devices.Service.Open
  -> devices.Handle.Media().Playback
  -> devices.Playback.Pump(ctx, audio.InboundMedia)
  -> runtime.RTCDeviceSink
  -> devicegw.DeviceSink
  -> audio.PlaybackQueue.Enqueue
  -> simulated callback: registry.Advance -> PlaybackQueue.RenderInto
```

The external consumer proves the two adjacent observations independently:
`PlaybackStats.QueuedSamples` is the queue-admission observation, while
`SimulatedDuplexRegistry.RenderedSamples` and `DeviceTraceEvent` are the
software device-callback observation after `Advance`. The deliberate wrong
oracle runs the same consumer with `C51_WRONG_ORACLE=1` and must fail before
the callback with the exact `wrong consumption oracle` marker.

Consumption labels are `QUEUE_ADMISSION`, `BUFFER_RECEIPT`,
`FILE_OR_SOFTWARE_RECEIPT`, `SOFTWARE_DEVICE_CALLBACK_CONSUMPTION`, and
`PHYSICAL_HARDWARE_CONSUMPTION`. The last is `OUT_OF_SCOPE` under the admitted
Windows-hardware/acoustic amendment. It is not converted to PASS by any
simulator or replay artifact.
