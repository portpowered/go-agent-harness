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

## CI rejection repair checkpoint

PR #414 at `cda45f34` was rejected by hermetic run `34376956947`, job
`102552070569`. The first failed control was
`TestRunRoom_ReportsClosedTargetAsRejectedPeerIngress` in
`agent-cli/internal/services/internal/agentruntime/session_room_audio_diagnostics_test.go:199`.
Under hermetic scheduling it observed the expected closed Bob mixer rejection,
then waited for the parent two-second deadline; cleanup reported Bob's connect
and observer plus Alice's observer as unfinished, and the room result was
`failed` with `ErrMixerClosed` leaked as Bob's generic fan-out error.

Repair commit `c91fd75` keeps the existing two-second parent deadline and
assertions, but makes the fixture causal: it waits for the `mixer_closed`
ingress record and Bob's participant-scoped error callback before canceling the
room. This exercises the normal cancellation, session-close, lifecycle and
observer-join path without racing failure attribution against parent timeout.
The control passed 20 normal and 10 race repetitions. Accumulated peer-ingress
controls passed 10 normal and 5 race repetitions; C21 gateway controls passed
three normal and two race repetitions, and focused vet plus `git diff --check`
passed. CI, independent review, merge and post-merge validation remain
unclaimed until the script-owned gate runs on the pushed head.
