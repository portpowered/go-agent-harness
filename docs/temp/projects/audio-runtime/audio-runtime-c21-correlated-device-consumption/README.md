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

## Static rejection repair checkpoint

The complete static job log for PR #414 head `a2fa99c8` is retained at
`/tmp/audio-runtime-c21-ci-static-job-102562722561.api.log`. Its only failed
step was `make architecture-size-check`, reporting four findings on
`TestRunRoom_ReportsClosedTargetAsRejectedPeerIngress`: cognitive and
cyclomatic baseline drift (`35`/`38` over the preserved `26` values), plus
`129 > 120` physical lines and `94 > 80` statements. The same four findings
reproduced locally; lint, vet and staticcheck completed successfully in the
job.

Repair commit `99428c75` moves only the added rejection/failure synchronization
into a bounded helper in the owned `session_room_audio_diagnostics_test.go`.
The existing test assertions, participant-scoped failure check, two-second
context and cancellation/join path remain intact; the named test is back at
its preserved `26`/`26` complexity baseline without editing the architecture
baseline. The repaired architecture gate reports `181` packages, `1860`
files and `27204` functions.

Focused causal evidence from the clean pushed head:

- normal and race `^TestC21` gateway tests passed;
- normal and race `TestRunRoom_ReportsClosedTargetAsRejectedPeerIngress`
  passed;
- `TestDeviceSinkSampleOnly*` and targeted runtime/room vet passed;
- `verify.py --mode all` was `ACCEPTED` in run
  `runs/verify-20260909T171841Z-51805`;
- `verify.py --mode public-consumer` was `ACCEPTED` in run
  `runs/verify-20260909T171910Z-52518`;
- `verify.py --mode parity` was `ACCEPTED` in run
  `runs/verify-20260909T171911Z-52529`, with `PROBE_TOOL_MARKER_9182`,
  `strict replay continuation`, `replay_complete`, and clean shutdown.

The exact source revision is `99428c75dd28ecc7a07c5d11ca7bc2a4144472cc`.
The consumer and YUI SHA-256 hashes are respectively
`10c457070b8e0722361465ef79e9862b0a3a60a094d1155552d04f16c537e511` and
`2739decfc2537dc3a90aae6a99e72424b8408fa788189e9424a45cd6117c0382`.
The fixture hashes are `38ed02805ce2dd0b7977e8e9ad2c0cf419d9632499e34fa601555384ef77f169`
(`c16-audio-tool`),
`154477d4086c47f707441e19489dfa1a21d493475b4163e64a2833dca3f17206`
(`c16-interruption`), and
`d7df42a198b9efd18be8fe3ce9b5cd329489a5087c589ab22d29ba2194b7a323`
(`expected`). Parity preserved `4800`-byte PCM SHA-256
`0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502` and
`3840`-byte PCM SHA-256
`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`.

No CI success, independent review, merge or vertical/project acceptance is
claimed; the same task remains responsible for any exact script-CI rejection.

## Current-head CI repair checkpoint

The canonical board was captured in `/tmp/audio-runtime-c21-board.json` before
this visit. It records task `work-task-46` on PR #414 at `cc0c172a`; no
concluded C21 review row was present. The complete CI rejection was inspected
with run `34382309620`: hermetic first failed on
`TestRunRoom_ReportsClosedTargetAsRejectedPeerIngress`, and also reported the
separate C20-owned remote `test46/provider_burst` final-PCM deadline; the
integration job reported the separate C22-owned
`TestSessionCLI_DuplexPCMMultiTurnSchedule` deadline. Those paths remain
separate ownership, not waived failures or reasons to mutate this lease.

The branch fetched `origin/main=5d5afcb1` and integrated it as merge
`17cc09a8`, preserving the required baseline ancestry and the C21 predecessor
checkpoints. Repair commit `e7605d95` adds a deterministic session-open gate
to the owned closed-target room fixture. Alice's scripted PCM cannot reach the
closed Bob mixer until both participants have registered `SESSION.OPEN`, so
the rejection tests participant-scoped cleanup rather than racing Bob's
connection/observer lifecycle registration. The original assertions,
participant-scoped error, two-second context, cancellation, and cleanup
budgets are unchanged; the architecture baseline was not edited.

Exact-head causal checks from `e7605d95442d53021a77c6976fc968d06f885420`:

- closed-target room: 20 normal and 10 race repetitions passed;
- provider-input rejection, ingress ledger, and room cleanup regressions: 3
  normal and 2 race repetitions passed;
- gateway `^TestC21`: 3 normal and 2 race repetitions passed;
- sample-only/runtime DeviceSink regressions passed;
- `go vet -tags=nomicrophone` for gateway/runtime and room passed;
- `make architecture-size-check` passed at 181 packages, 1860 files, and
  27215 functions;
- `verify.py --mode all` was `ACCEPTED` in
  `runs/verify-20260909T174016Z-65947`; every child stayed below its 60-second
  process-group deadline and the runner recorded exact stdout/stderr, argv,
  cwd, environment, exit code, and elapsed time.

The exact-head public evidence reports zero samples while callbacks are
paused, native device rate 16000, primary `LastSequence=13` and
`DeviceSamples=16`, plus stalled-consumer `DroppedObservations=599`,
`DroppedSamples=599`, `MetadataLostSamples=45`, `LastSequence=600`, and clean
close. The same-source consumer/YUI hashes are
`10c457070b8e0722361465ef79e9862b0a3a60a094d1155552d04f16c537e511` and
`2739decfc2537dc3a90aae6a99e72424b8408fa788189e9424a45cd6117c0382`.
Parity retained the C16 fixture hashes and exact PCM hashes: audio-tool
`4800` bytes / `0e769b4aa4a4532ee188a966ec485fb98d0938bcb77bceac7a85edce15b92502`
and interruption `3840` bytes /
`6c0dbccd178ab1bcc005bc756c548f28f3888e265a46c11fe66bece28c539e22`.

This is a repaired implementation candidate, not CI/review/merge or
post-merge vertical/project acceptance. Next action: commit this evidence
checkpoint, push the same branch, update PR #414 with exact head/base and
repair evidence, and return `ACCEPTED` to the script-owned CI gate without
polling it. Retain this task for any exact C21-owned rejection.

## Residual duplex EOF repair checkpoint

The current-head CI rejection for PR #414 at `84558cdc` was inspected from
the complete saved job log `/tmp/audio-runtime-c21-ci-coverage-34384515490-api.log`.
It failed only
`TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnCommitControls/missing_commit`:
both harnesses completed three turns and all six crossings, but harness B
reported `Clean=true`, `FinalTick=10`, `OutputFrame=true`, and
`InputEOF=false`; harness A completed cleanly. This is distinct from the
earlier absent-tail/deadlock signature and is not waived or treated as an
intermittent pass.

The causal fixture defect was the bridge reader's simultaneous
`ctx.Done()`/queued-EOF select. Provider close can cancel the audio source
after the final bridge writer has published EOF; the old select could return
cancellation before consuming that already-published packet. The repair in
`session_duplex_overlap_bridges_test.go` first drains an available packet and
rechecks the queue when cancellation wins. EOF accounting remains in the
single packet-consumption helper, so `observedEOF` is set only after the
reader actually returns `io.EOF`; queued EOF is never labeled consumed by
publication alone. A focused
`TestV8MultiTurnBridgeReadConsumesQueuedEOFAfterCancellation` regression pins
the ordering.

Post-repair causal evidence from the same worktree: the focused EOF regression
and exact missing-commit control passed; the complete multi-turn family passed
normal and under `-race`, including six crossings and PCM/transcript/commit
negative controls. C21 gateway normal/race, sample-only DeviceSink, closed-
target room normal/race, targeted vet, `git diff --check`, and the architecture
gate also passed. The architecture gate reports 181 packages, 1860 files, and
27218 functions without baseline changes. Existing public consumer/YUI/parity
evidence from `e7605d95` remains unchanged because this repair is limited to
the admitted duplex fixture path. CI, independent review, guarded merge and
post-merge validation remain external.
