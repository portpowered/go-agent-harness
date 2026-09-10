# C25 extraction plan: one room audio dependency

This is a source-boundary plan, not an implementation or acceptance claim. C25
owns the diagnosis, verifier, and evidence packet only; it makes no production
change.

## Chosen dependency

The one dependency selected for extraction is the **legacy room clock/format/
device-output boundary**: `agent-cli/internal/room.PCM16Mixer` still owns frame
cadence and PCM16 adaptation, while the legacy room runner owns resampling and
direct device output. Accepted C26 moved PCM16 accumulation and final clipping
to `go-audio/pkg/mixer.MixPCM16Samples`; C25 treats that historical DSP seam as
eliminated and does not report it as a current gap.

The source gap is concrete even though the public `yui room run` route is now
wired to `go-agent-runtime/services/rooms`: the legacy `RunRoom` implementation
remains compiled and exercised by its own internal package tests. The
`servicetest` package imports the implementation for other session seams but
does not export `RunRoom` (see `runtime.go:109-112`). The hosted C25
rejection is a separate C20-owned remote-device scenario; C25 does not infer
that the legacy path caused it.

## Current path and ownership

1. `agent-cli/internal/services/internal/agentruntime/session_room_orchestration.go:161`
   creates `room.NewPCM16MixerWithConfig` for every participant and stores it in
   `roomParticipantRuntime.mixer` (`session_room_lifecycle.go`); the same file
   opens `devicegw.NewDeviceSource` and `devicegw.NewDeviceSink` at lines
   489-499.
2. `session_room_run.go:947` reads `ReadFrameWithSources` to send human input
   to the provider; `:1049-1074` reads/resamples device input and fans out raw
   PCM bytes; `:1116-1125` reads the mixer for human speaker output.
3. `session_room_run.go:1176-1186` decodes the mixer-produced PCM16 bytes,
   resamples them to the device rate, accumulates native frames, and calls
   `devicegw.DeviceSink.WriteFrame` at `:1186`.
4. `agent-cli/internal/room/mixer.go:167` constructs `time.NewTicker`;
   `:270` constructs the legacy mixer; `:743` and `:755` perform PCM16 format
   adaptation around the shared `audiomixer.MixPCM16Samples` operation at
   `:751`, with bounds validation at `:725` and no local accumulation/final
   clipping loop.

The current implementation owner is the legacy agent-runtime/room path's
follow-on maintainer. C25 is not authorized to edit it. C21 retains the
room/gateway/duplex boundary, C18 retains exact lifecycle work, C20 retains
the separate `test46/provider_burst` production/fixture repair, and C26's
accepted shared-DSP merge is the historical elimination evidence. Any future
patch touching those boundaries must be coordinated with the relevant primary
task.

## Proposed boundary and APIs

Keep the public wire and room service stable, but replace the selected
room-owned mixer dependency with the canonical frame/timing boundary:

- `go-audio/pkg/mixer.New(ctx, scheduler clock.TimerSource, config)` owns
  cadence and returns `mixer.Mixer`.
- `mixer.Mixer.AddInput` returns a bounded `mixer.Input`; callers submit
  `audio.PCMFrame` with `Input.WriteFrame(ctx, frame)`. The input uses
  `audio.NewFrameBuffer`, preserves `Epoch`/`EndOfResponse`, and never accepts
  a raw device or provider callback.
- `go-audio/pkg/clock.RequireTimerSource` is the explicit scheduler contract;
  no room implementation constructs a host ticker for this dependency.
- The canonical room graph uses `rooms.MediaPorts` (`MediaCapture` and
  `MediaPlayback`) and `MediaFactory.OpenMedia`, then joins those workers in
  `go-agent-runtime/services/rooms/internal/lifecycle/media.go`.
- Device conversion and callback ownership remain in the device service. The
  `RTCDeviceSink.Pump` path uses the bounded playback queue, whose
  `PlaybackQueue.RenderInto` reports callback consumption and zero-filled
  underflow separately from queued samples. The room graph's
  `audio.FrameProducer` playback queue remains an admission boundary, not an
  acoustic or physical-device claim.

## Wire and sequencing

The public construction sequence is preserved:

`wire_gen.go:109` → `NewRoomServiceWithDevices` → room-wire
`Dependencies{Live, Devices, Registry, Clock}` → `rooms.Service` → lifecycle
`Runner`.

The extraction sequence is:

1. Add the adapter at the legacy room boundary, translating each admitted
   input/output unit to `audio.PCMFrame` and accepting an injected
   `clock.TimerSource`; keep the public wire unchanged.
2. Route peer/provider input through canonical `mixer.Input.WriteFrame`, with
   explicit epoch invalidation and response-boundary markers before any local
   playback admission.
3. Route local capture/playback through `rooms.MediaPorts` and the media
   bridge; leave device selection and `RTCDeviceSource`/`RTCDeviceSink` in the
   device service.
4. Assert callback-consumed versus queued samples at the device boundary, then
   remove the legacy ticker/raw-codec/direct-sink dependency only after the
   focused controls and the public-consumer regression pass.

## Trigger, behavior, and failure control

Trigger: source inspection finds `time.NewTicker`, PCM16 format-adaptation
calls around the accepted shared mixer, direct device constructors, and direct
`DeviceSink.WriteFrame` in one legacy room implementation. The current hosted
failure is separately recorded as
`TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst`
at `session_tool_audio_remote_e2e_test.go:182`: the evidence snapshot request
exceeded its 30-second context deadline. C20 owns that repair; C25's local
single and 12-subtest controls did not reproduce it.

Current behavior is host-ticker cadence over raw bytes, with format adaptation
around the shared mixer and room orchestration performing resampling/decoding
before writing device frames directly. Intended behavior is injected-clock
cadence over `audio.PCMFrame`, bounded epoch-aware
buffers, and a device-owned callback queue that distinguishes admitted,
consumed, discarded, and zero-filled samples.

Executable regression and controls:

- source oracle must report `SOURCE_GAP_CONFIRMED` with all required edges;
- positive canonical fixture must be accepted and ticker/codec/direct-device
  negative fixture must be rejected;
- stale source hash, false-success output, truncated output, zero test
  discovery, child timeout, process-group cleanup, and aggregate budget are
  harness failures, not evidence of a runtime pass;
- selected tests are `TestMixerPreservesShortResponseTailAndEpochs`,
  `TestPlaybackRenderObserverIncludesCallbackSilenceOutsideQueueLock`,
  `TestRequireTimerSourceDoesNotFallbackToHostTime`,
  `TestRoomGraphRoutesEachSourceToPeersOnly`,
  `TestRoomGraphRecordsReceivedOnlyAfterProviderAdmission`,
  `TestMediaBridgePreservesPCMAndUsesBoundedFrames`, the C26 shared-DSP
  reconciliation seam, the legacy mixer seam,
  the empty-response agent-runtime seam, its race run, and focused vet.

The verifier records native return codes separately from harness verdicts,
timestamps every child, bounds each child to at most 60 seconds and the
aggregate to at most 600 seconds, and terminates/reaps the process group on a
hang. It does not retry or weaken a failed assertion.

## Why this does not duplicate C15-C17

C15's replay evidence, C16's sink lifecycle evidence, and C17's deterministic
clock/PONG evidence are consumed as limitations and regression inputs. This
plan adds no provider session, physical device, acoustic, or broad replay
claim. It targets the remaining compiled legacy dependency and verifies only
source edges, canonical dependency shape, selected software boundaries, and
runner integrity.

## Current exact-head handoff checkpoint

Historical candidate references above remain preserved as predecessor evidence.
The freshly fetched `origin/main` is `c95a2cb4f96fa8c14bd4655f5197a822c86a980c`;
the isolated C25 merge candidate was `b957dd96224f76a4919960413d4a6dcbc1dc6bb1`,
and the tested evidence source candidate is
`8c61326167ceb6ed2017b46ff866656d99a711b7`. The final metadata carrier is an
evidence-only descendant with identical executable inputs. The required baseline, startup
integration, and accepted C26 merge ancestry remain intact.

The exact tested-source verifier run returned `ACCEPTED` in `16.671s` across `27`
bounded commands with `10` focused normal/race/vet regressions. The source gap,
AST positive/negative/mutation/comment controls, fixture manifest, rebuilt and
materialized archive, changed-path allowlist, zero-test handling, intentional
child-hang cleanup, and aggregate shutdown all passed with no surviving child.
The current source archive is `57,067,520` bytes with SHA-256
`5e8cb07b1bc2ef3b999ca57458100bb6620697a0af481c28436493f8a3737a12`.

The prior hosted PR417 rejection remains historical and separately owned by
C20; no CI-green, runtime, physical-device, acoustic, or project-completion
claim is made here. Submit this changed same task to the script CI gate after
the remaining evidence metadata is synchronized, without polling CI.

## Gate map and later handoff

- `AUDIO`: `PCMFrame`, `FrameBuffer`, canonical mixer input, epoch and
  end-of-response sequencing.
- `DEVICE`: `RTCDeviceSink`, `PlaybackQueue`, callback render/underflow and
  consumed-sample observations.
- `SERVICE`: `MediaPorts`, `MediaFactory`, lifecycle media bridge, and stable
  public wire.
- `QUALITY`: source/path provenance, positive/negative controls, focused
  regressions, race/vet, first-failure and cleanup evidence.

The reviewed C20 composition repair is now present on the fetched
`origin/main`; the old coverage assertion passes in the focused current-main
control. The separate C20 `test46/provider_burst` remote scenario remains
historical and unwaived. C25 should refresh this packet against the same
task/head, run the bounded focused verifier, commit/push the evidence-only
change, and submit the same PR to the script CI gate. No C25 document claims
green CI or acceptance before those later gates.
