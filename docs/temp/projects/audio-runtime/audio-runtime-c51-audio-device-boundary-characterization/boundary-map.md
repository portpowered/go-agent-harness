# C51 audio/device boundary map

The characterized source is the fetched integrated main
`6b4f32b5940d43192774e86f05e3c7c9293c71e3`; the planning-main checkpoint is
`7f73c8b3b4ebc99b55b8bb5e802beff024385407`. `import-graph.json` is generated
from production Go imports with file/line citations and checked with
`GOWORK=off go list -json ./...` for every workspace module.

`boundary-map.json` is the machine-readable responsibility map. Each of its
nine responsibilities has:

- an owning package and downstream consumers;
- exact pinned-source citations for the owner API;
- explicit production caller citations with enclosing function names (not tests,
  comments, fields, or declarations mislabeled as calls);
- a named timing domain and evidence strength; and
- an evidence disposition that does not infer hardware consumption from queue
  admission or software callback output.

The verifier reads each cited line from the pinned Git revision and requires
the recorded symbol and source needle to occur on that line. A stale,
out-of-range, comment-only, or unrelated citation fails closed.

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

Consumption labels are `QUEUE_ADMISSION`, `BUFFER_RECEIPT`,
`FILE_OR_SOFTWARE_RECEIPT`, `SOFTWARE_DEVICE_CALLBACK_CONSUMPTION`, and
`PHYSICAL_HARDWARE_CONSUMPTION`. The last is `OUT_OF_SCOPE` under the admitted
Windows-hardware/acoustic amendment. It is not converted to PASS by any
simulator or replay artifact.
