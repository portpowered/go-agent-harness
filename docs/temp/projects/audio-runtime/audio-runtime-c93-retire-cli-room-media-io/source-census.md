# C93 pre-mutation source census

This checkpoint is candidate-independent. It was recorded on 2026-09-12
before changing any C93-owned implementation path.

## Admission and identity

- Project/contract: `audio-runtime` / `audio-runtime-v1`.
- Task: `audio-runtime-c93-retire-cli-room-media-io`.
- Admission: `project-control.py verify-work --type task --name audio-runtime-c93-retire-cli-room-media-io --root "$FACTORY_ROOT"` returned `status=admitted`.
- Branch/worktree: `codex/audio-runtime-c93-retire-cli-room-media-io` / the isolated C93 worktree; `prd.json.branchName` matches exactly.
- Baseline source revision: `3d3e72786ac6fc1fd47c7e029589e5117674b035`.
- Fetched `origin/main`: `3d3e72786ac6fc1fd47c7e029589e5117674b035`.
- Required startup integration: `8bdafc7f947a3a2c9856220abdc539437035bd21` is an ancestor.
- Planning revision: `3d3e72786ac6fc1fd47c7e029589e5117674b035` is an ancestor.
- No second project, acceptance waiver, host-checkout reset, or local pre-existing edit was observed.
- No prior C93 review finding or review Work exists in the canonical `~default` task record; the setup attempt has no transcript, and the active task is `work-task-169`.

## Frozen source

`agent-cli/internal/services/internal/agentruntime/session_room_run.go` is
exactly 1,192 physical lines at the baseline. Its SHA-256 is
`2ac0ff50ee59a1ec8f04682e04d876d270146ecda8245c5d14d28a5f7120e8d6`.

The direct owned test baselines are:

- `session_room_human_test.go`: `c2772a4b67d52727b09f2b9ba18d3b418858c6d611cbb2a68d073acb1d4cbc78`.
- `session_room_playback_diagnostics_test.go`: `8abeb48960ec6afa28703ef189f60754cdee51caefd3278acde14108caba0693`.

The seven named legacy regions and baseline hashes are:

| symbol/region | lines | SHA-256 |
| --- | ---: | --- |
| `pumpRoomMixer` | 95 | `6f6ef8fcb95c4122fff1f74d8e3158685c4956783dd09ff0d52c05d8824774d9` |
| `roomProviderInputPCM` | 18 | `42f4ae31deda44a7e2486d95db90e87232663bad64683ad21910944385e96163` |
| `runRoomHumanCapture` | 71 | `1754b128b6b29acd1c6655cd97f4c876d2b01b1f9f24a1d78eb52b1782d57caa` |
| `pumpRoomHumanOutput` | 47 | `fa787ec674d75d7866f9552c798ee4638da5699aa70bd9cb6f46da4b0eb11fc1` |
| `roomHumanOutputClock` | 11 | `3cb0bef144ea0f46a66fd18957ee7480c9c92024dbb9ed5fd66d201f3d52c97e` |
| `encodeRoomPCM16` | 4 | `703b95a1882b817e2a0e29e8fb32e27ba6c393f4abf7ac800bb6de2fc3f39d85` |
| `roomHumanOutputBuffer` and `writeFrame` | 34 | `f9aab139385337337cf81772a5f883ceffb54516f9e86c64824fdff894b6c969` |

## Caller and dependency census

- `runRoomParticipant` starts `pumpRoomHumanOutput`, `pumpRoomMixer`, or
  `runRoomHumanCapture`.
- `roomProviderInputPCM` is called by `pumpRoomMixer` and directly by
  `session_audio_rate_test.go`.
- `encodeRoomPCM16`, `roomHumanOutputClock`, and `roomHumanOutputBuffer` are
  only referenced from the owned file at baseline.
- `pumpRoomMixer` depends on `room.PCM16Mixer.ReadFrameWithSources`,
  `agentloop.AgentLoop.SendAudioInputWithPolicy`, coordinator audio-input
  policy, ingress resolution, participant evidence, replay acknowledgements,
  injected input observation, cancellation, and participant failure wrapping.
- `roomProviderInputPCM` depends on the mixer sample rate, participant
  provider sample rate, PCM16 codec/resampling, and conversion error identity.
- `runRoomHumanCapture` depends on `DeviceSource.ReadFrame`, the participant
  mixer format, active peer selection, peer route admission, sent/fan-out
  evidence callbacks, cancellation, and participant failure attribution.
- `pumpRoomHumanOutput` depends on mixer source attribution, the participant
  `DeviceSink`, hold-tone timing, ingress resolution, received evidence,
  cancellation, and participant failure attribution.
- `roomHumanOutputClock` is the only room-media clock seam: it uses the
  participant's injected clock and falls back to the explicit real source.
- `roomHumanOutputBuffer` depends on mono PCM16 validation, codec decoding,
  mixer-rate to 16 kHz conversion, fixed 480-sample device frames, and
  bounded writes that preserve an incomplete conversion tail for later input.

## Candidate-independent behavioral oracles

- Provider input: source IDs and policy are copied; conversion preserves
  little-endian PCM16 bytes and exact supported-rate lengths; send precedes
  resolution/observation/ack; rejected frames resolve once as rejected; a
  successful frame resolves once after send; `errors.Is`/`errors.As` identity
  survives send, mixer, observer, conversion, and cancellation failures.
- Human capture: input frames are mono PCM16, resampled once per target rate,
  encoded without padding or aliasing, never fanned back to the source, and
  sent to the active target set in deterministic order. A target write error
  is attributed to the target while a terminated target is ignored.
- Human output: the injected clock drives hold-tone decisions; non-silent
  bytes remain exact; silent output never becomes a queue-consumption claim;
  output writes are converted to exact 480-sample 16 kHz frames; partial
  conversion tails stay buffered and are not duplicated or padded.
- Failure/shutdown: EOF and cancellation are bounded normal exits where the
  legacy adapter classified them as normal; mixer/device/send/observer errors
  retain identity, participant attribution, and redaction; no goroutine or
  second close is introduced by the service.

## Ownership snapshot

C79 retains the shared Wire registry and architecture-size baseline lease.
C84/C85/C88/C90/C91/C92 retain their disjoint room/lifecycle/replay/capability/
audio-output/capture-claim paths. C64 retains provider-audio terminal drain.
C93 therefore changes only the named C93 regions, the new `roommedia` service,
its coverage entries, and this evidence folder until those shared leases are
released.
