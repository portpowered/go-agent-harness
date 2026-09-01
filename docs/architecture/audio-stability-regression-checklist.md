# Audio stability regression checklist

This checklist turns the audio-choppiness design review into merge gates. “Covered” means the behavior is either exercised by deterministic PR tests, by the existing full session/replay corpus, or by an explicit opt-in OS/hardware lane. Hardware-only observations are never claimed by the hermetic lane.

## Component changes

- [x] Preserve `VirtualRegistry` as the exact, timeless transport backend.
- [x] Add `SimulatedDuplexRegistry` with explicit callback advancement, variable quanta, deterministic jitter metadata, clock epochs, faults, bounded playback/capture queues, render/capture taps, and deterministic acoustic mixing.
- [x] Replace callback-time playback slice compaction with a fixed-capacity ring.
- [x] Count callback renders, rendered samples, underrun events/samples, zero fill, and minimum callback queue depth.
- [x] Remove per-chunk playback drains; retain capacity hysteresis and response-end/shutdown drain boundaries.
- [x] Make capture overflow explicitly `drop_oldest`, count every dropped frame/sample and sequence gap, and test the former drop-newest regression.
- [x] Add a stateful PCM16 resampler with rational phase, bounded FIR history, anti-alias filtering, exact final count, reset, and explicit end behavior. Keep the legacy live boundary unchanged until recorded replay fixtures can be versioned; switching it in place changed feedback-gate behavior and invalidated strict captures during verification.
- [x] Add deterministic acoustic delay/gain/FIR, near-end, and background stems without collapsing the original sources.
- [x] Add atomic, finalized failure capsules containing scenario, PCM stems/taps, JSONL device trace, sizes, and SHA-256 hashes; validate and replay them.
- [x] Add `make test-audio-stability` and a focused race corpus in CI.
- [x] Add application-owned `MetricSampler` and structured `Logger` function adapters with no-op defaults, defensive field copies, and panic/error containment.
- [x] Register both observability seams as required/defaulted live Wire ports; prove replacement identity, displaced-default suppression, and propagation through the generated session graph.
- [x] Export final playback and capture queue snapshots outside native callbacks, including underrun/zero-fill, overflow/drop-oldest, discard, watermarks, callback totals, and capture sequence gaps.
- [x] Export simulated callback/fault/clock-epoch evidence and RTC start/failure/close lifecycle records through the same ports used by production sessions.

## Failure matrix coverage

The matrix is covered proportionally rather than by one test function per row; the named tests below are the owning tables/corpora.

- [x] T01–T03, T16–T18 — clean baseline, fixed/alternating/partial/zero/terminal callback segmentation: `TestSimulatedDuplexCleanBaselineAndVariableCallbackQuantum`, existing partial-frame session tests.
- [x] T04–T06 — deterministic small/burst jitter and stall/underrun accounting: `TestSimulatedDuplexClockJitterFaultsAndEpochsAreDeterministic`, `TestSimulatedDuplex48kMacCadenceAndExactUnderrun`.
- [x] T07–T15 — startup gaps, drift metadata, jumps, reset, missing/duplicate/reordered policy and epochs: simulator fault table and trace assertions. Duplicates are diagnosed and rejected, never forwarded twice.
- [x] T19–T20 — startup/lifecycle ordering and long integer-sample timelines: deterministic render-first ordering, duration table, long-run resampler count, and session lifecycle corpus.
- [x] B01–B10 — exact underflow/overflow, capture policy, starvation, burst pacing, hysteresis and capacity edges: playback queue, simulated duplex, and `TestVirtualPlaybackCapacityAdversarial` tables.
- [x] B11–B16 — malformed/fragmented input, diagnostics/recorder pressure, callback contention accounting, and defensive ownership: device, recording short-write, queue, and RTC adversarial suites.
- [x] B01–B16 observability — `TestSessionPlaybackObservabilitySamplesCompleteSnapshotAndContainsFailures`, `TestSessionCaptureObservabilitySamplesDropOldestLoss`, `TestSimulatedDuplexObservabilityReportsFaultsOutsideDeviceLock`, and `TestRTCDeviceBindingPublishesCaptureSnapshotAfterCallbackStops` lock the stable metric/log schema and prove observers execute after callback locks are released.
- [x] B17–B25 — mid-frame cancel, linearizable barge-in discard, stale generations, close/loss/double-start/repeated-close/stress/race: RTC cancellation/adversarial suites plus `test-audio-stability-race`.
- [x] R01–R09 — identity, complete supported-rate matrix, chunk invariance, duration, independent count oracle, phase continuity, long-run count and final tail: streaming resampler matrix and RTC boundary tests.
- [x] R10–R16 — alias rejection, passband preservation, FIR/step/silence/DC/extreme saturation behavior: `TestDownsample48To16RejectsOutOfBandAlias`, streaming signal tests, and existing PCM analysis corpus.
- [x] R17–R22 — declared/actual rate and format changes, unsupported rates/channels/encodings, and queued-format isolation: device-format conformance, replay rate validation, and unsupported conversion tests.
- [x] A01–A14 — delay/gain/reference evidence, near-end, double-talk, noise and late/duplicate/low-level references: deterministic acoustic simulator plus existing feedback gate suppression/bypass suites.
- [x] A15–A23 — nonlinear/clipped/mixed paths, polarity/frequency shaping, saturation and source envelope boundaries: integer acoustic/FIR and existing mixer/PCM property suites.
- [x] A24–A36 — first playback, acoustic tail, correlated speech, ±100 ms boundary, release pressure, rate/topology/reset/state-bleed/silent/false-reference behavior: existing RTC feedback, self-hearing, room overlap, hold-tone and lifecycle suites. These are gate oracles, not claims of adaptive AEC subtraction.
- [x] P01–P07 — schema/finalization/size/hash/truncated PCM/WAV/rate validation: failure capsule integrity tests plus session/room manifest tests.
- [x] P08–P16 — final tail/padding/edge silence/short write/storage failures/event gaps/time/cross-tap alignment: recording, WAV analysis, and replay divergence suites.
- [x] P17–P24 — repeatable replay, logical time, queued-vs-rendered evidence, partial capture, large/provenance/cross-platform/diagnostic pressure: capsule round trip, deterministic trace/hash assertions, and existing session recording corpus.

## Native and hardware lanes

- [x] M01–M08 — CoreAudio default/permission/busy/format/loss/route/sleep/Bluetooth contracts are represented by typed platform errors and the opt-in CoreAudio hardware conformance lane.
- [x] M09–M14 — digital loopback, drift/overload/period/partial callback, and queue-empty-versus-rendered checks are owned by `TestRTCDeviceBindingHardwareRoundTrip`, CoreAudio callback tests, and the deterministic simulator used as the PR negative/control lane.
- [x] M15–M20 — physical volume, competing apps, service restart, electrical/acoustic loopback and background system audio remain opt-in hardware-canary scenarios; their committed runner is the existing device probe/hardware round-trip path and their artifacts use the same quantitative recording/replay analyzers.

Environment-gated M-lane tests require the named macOS route, permissions, and (for electrical/acoustic cases) physical fixtures. A green hermetic run proves the implementation and deterministic controls; it does not assert that absent hardware was exercised.

## Required merge commands

- [x] `make test-audio-stability`
- [x] `make test-audio-stability-race`
- [x] `make fmt-fix`, `make vet`, and `make build`
- [x] `make coverage-changed COVERAGE_BASE=origin/main`
- [x] Root `make test` audit: all changed packages passed; the existing `TestSessionCLI_DuplexPCMMultiTurnSchedule` timeout was reproduced unchanged in a clean `origin/main` worktree and is therefore recorded as a baseline exception, not hidden as a branch regression.
- [x] `make validate` component gates relevant to this change (format, vet, build, changed coverage, focused race) passed. The aggregate target inherits the same independently reproduced baseline integration timeout above.

The observability follow-up reruns these merge commands on its own branch before merge; checklist marks describe required gates, not inherited results.

No hardware-only M-lane result is inferred from these hermetic gates.
