# C21 correlated device-consumption evidence

This lease adds the public pull boundary on `runtime.RTCDeviceSink`:

```go
subscription, err := sink.SubscribePlaybackObservations(128)
observation, err := subscription.Next(ctx)
```

`RTCDevicePlaybackObservation` uses the selected device's native PCM16 sample
clock. `StartSample`/`EndSample` are half-open device-sample ranges, and
`SampleRate` is the device rate; neither field is provider-rate or wall-time
metadata. `PlaybackResponse` reuses the canonical `audio.PlaybackResponse`.

Admission receipts are separate from callback receipts. `consumed` is precise
model PCM rendered by the backend callback; `underflow` is actual callback
zero-fill and is not response consumption; `hold_tone` and `cue` are local
content; `discard` is queued audio removed by interruption/cancellation; and
`unattributed` means the callback was real but response metadata was not
available. `Consumed` means the device callback advanced for the observation;
the `Kind` field identifies whether those samples were model audio.

Delivery is pull-only and nonblocking on the native callback. The default
subscription queue is 128 observations and the public maximum is 4096. The
correlation ledger retains at most 256 segments and 8192 sample positions;
event PCM is bounded at 4096 samples. `DroppedObservations`, `DroppedSamples`,
`MetadataLostSamples`, and `LastSequence` make diagnostic loss visible; a
full consumer never blocks audio or starts a per-observation goroutine.

The external consumer is built by file argument because `docs/temp` is not a
module in the existing `go.work`; adding a module manifest or editing the
workspace would violate the C21 lease:

```sh
rtk proxy go build -tags=nomicrophone \
  -o artifacts/consumption-consumer consumer/main.go
rtk proxy ./artifacts/consumption-consumer
rtk proxy python3 verify.py --mode all
```

`verify.py` also supports `public-consumer` and `parity`. Every child process
has a 60-second process-group deadline and retains its exact command, cwd,
selected environment, raw stdout/stderr, exit code, and elapsed time under
`runs/`. The parity mode copies and replays the read-only C16 audio/tool and
interruption fixtures from their recorded SHA-256 provenance; it does not
modify the C16 or C20 evidence leases.
