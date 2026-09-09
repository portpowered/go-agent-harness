# C25 evidence ledger

## Admission and ancestry

- Factory admission command: `python3 $FACTORY_ROOT/factory/scripts/project-control.py verify-work --type task --name audio-runtime-c25-audio-device-boundary-diagnosis`.
- Admission result: `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c25-audio-device-boundary-diagnosis"}`.
- PRD branch: `codex/audio-runtime-c25-audio-device-boundary-diagnosis`; isolated worktree and `git branch --show-current` agree.
- `origin/main` after refresh: `98ce636dd67349ba64f22cd7916dd370cf4ba484`; it was merged into the isolated candidate at `88f996c5384222d97b92578a6ba7f1ea83adbf2e`. The latest pre-repair evidence baseline is `8e96dc077744d204994274b22356af8e856a8aa7`; earlier `67265932` and later documentation successors remain preserved checkpoints.
- Baseline ancestry: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` is an ancestor of the admitted head.
- Integration ancestry: `8bdafc7f947a3a2c9856220abdc539437035bd21` is an ancestor of the admitted head.
- Refreshed source archive SHA-256: `e77933bdc0f47a2f885581eb9b51a297b763f7a7b54bffe3b07254fa2126f11e`.
- The canonical board had task row `work-task-71` in `PROCESSING`, plan row `work-plan-70` complete, concluded review row `work-review-78`, and the task-row CI rejection for PR417 at `1040f92478bd1da6450f0f9a1fc1f4532ff34937`. C18/C21 review findings were read and remain outside this owned path.

## Prior accepted evidence

- C15 `docs/temp/projects/audio-runtime/c15-probe-result-reconciliation/report.json`: accepted relative/absolute/nested/dot-dot software replay and fail-closed unsafe inputs; no physical/acoustic/live proof.
- C16 `docs/temp/projects/audio-runtime/c16-probe-result-reconciliation/report.json`: accepted software sample-only sink partial/full/cancel/error/close behavior; actual physical consumption remains unproved.
- C17 `docs/temp/projects/audio-runtime/c17-probe-result-reconciliation/report.json`: accepted injected canonical PONG clock/compatibility/clean cancellation; broader parsing, DSP, sample timing, buffer centralization, and device I/O remain open.

## Causal source evidence at the admitted head

| Evidence | Exact source | Lines | Interpretation |
| --- | --- | ---: | --- |
| Host cadence | `agent-cli/internal/room/mixer.go` | 11, 167, 224 | Legacy `time.Ticker` and `PCM16Mixer` own cadence. |
| Local PCM codec | `agent-cli/internal/room/mixer.go` | 13, 736, 760 | Legacy room mixer decodes/encodes PCM16 locally. |
| Device construction | `agent-cli/internal/services/internal/agentruntime/session_room_orchestration.go` | 161, 494, 499 | Same legacy orchestration constructs mixer, source, and sink. |
| Direct output path | `agent-cli/internal/services/internal/agentruntime/session_room_run.go` | 1049, 1057, 1074, 1116, 1176, 1180, 1186 | Human output resamples/decodes and writes sink frames directly. |
| Canonical timing/buffer graph | `go-audio/pkg/mixer/mixer.go`, `go-audio/pkg/mixer/input.go`, `go-audio/pkg/clock/clock.go`, `go-audio/pkg/audio/buffer.go` | 15, 45, 58; 28, 80; 34, 238, 390; 53, 101 | Injected `clock.TimerSource`, `audio.PCMFrame`, and bounded epoch-aware `audio.NewFrameBuffer`. |
| Canonical room boundary | `go-agent-runtime/services/rooms/contract.go`, `.../lifecycle/graph.go`, `.../lifecycle/media.go`, `.../lifecycle/participant.go` | 95, 118; 66, 124; 22; 20, 35 | Room owns lifecycle while media ports, media bridge, and mixer own media behavior. |
| Device boundary | `go-audio/pkg/audio/device_playback.go`, `go-audio/pkg/audio/device_playback_test.go`, `go-device-gateway/pkg/runtime/rtc_device.go`, `.../rtc_device_sink.go` | 51, 236; 190, 209; 105, 143, 387 | Device runtime owns the bounded playback queue/callback and distinguishes queued from consumed/zero-filled samples. |
| Public route and service-test boundary | `agent-cli/internal/wire/wire_gen.go`; `agent-cli/internal/transport/cli/room.go`; `agent-cli/internal/services/servicetest/runtime.go` | 109-110; 52; 109-112 | `yui room run` receives public `runtimeRooms.Service`; `servicetest` imports the legacy implementation for session helpers but exports no `RunRoom`. |

The verifier stores SHA-256 hashes and line counts for all audited files in `provenance.json`.

## Dependency and regression evidence

The refreshed `verify.py --mode all` passed at pushed candidate `0eb46135bdeb3f2976e4658a02a6f39cf1d47459` in 10.325 seconds:

- positive canonical dependency control: `ACCEPTED`;
- negative host-ticker/local-codec/direct-gateway control: `REJECTED_AS_FORBIDDEN`;
- `go list` for legacy room, legacy agentruntime, public CLI, canonical mixer, and public rooms packages;
- canonical mixer, playback, clock, and room lifecycle/media-bridge tests: all `ACCEPTED`;
- `go test ./pkg/agentloop -run '^TestProductionAudioAndDeviceOwnership$' -count=1 -timeout=45s`: `ACCEPTED`;
- `go test ./internal/room -run '^TestPCM16MixerMixesEveryActiveInputAndClips$' -count=1 -timeout=45s`: `ACCEPTED`;
- `go test ./internal/services/internal/agentruntime -run '^TestRunRoom_EmptyResponseDoesNotAdvanceTurnsOrMaxTurns$' -count=1 -timeout=45s`: `ACCEPTED`;
- the same agent-runtime behavior test with `-race`: `ACCEPTED`;
- `go vet ./internal/services/internal/agentruntime`: `ACCEPTED`.

The source ancestry and audited source-path diff both returned 0. Positive canonical dependency control was `ACCEPTED`; the negative host-ticker/local-codec/direct-gateway control was `REJECTED_AS_FORBIDDEN`. Stale hash, false-success output, truncated output, and zero-test/child-hang cleanup controls were rejected/accepted as designed; the intentional child hang returned native `-15`, timed out at 1 second, and left no children. Clean shutdown was true. This is source/package evidence only; it is not a physical-device or acoustic PASS.

## Current-head CI rejection characterization

- The full raw completed log is saved as `/tmp/factory-c25-ci-integration-34397773527.log` (43,401 bytes; SHA-256 `22092c58d28a21040259b7317c695c94d4bbd8b18fe38ef31aa9c22bda580b02`). Run `34397773527`, job `102621627290`, rejected PR417 at `1040f92478bd1da6450f0f9a1fc1f4532ff34937` in `make test-integration`.
- The first and only failing assertion was `TestAgentBinaryToolContinuationPreservesRemoteDeviceAudio/test46/provider_burst`: the `waitForRemoteToolAudio` phase had stopped the callback clock after `allResponsesSent`, then `ReadRemoteDeviceServerSnapshot` exceeded the existing 30-second scenario context and returned `context deadline exceeded` at `session_tool_audio_remote_e2e_test.go:182`. The child was not yet in its final close path; `ReleaseClose` follows snapshot validation for this case.
- The adjacent production-binary audio-device replay passed in 80.567 seconds and the committed replay/fixture regression package passed in 9.617 seconds. The hosted evidence therefore demonstrates a snapshot/evidence-phase timeout, not provider response loss, a source-path compile failure, or a proven PCM/EOF cause.
- A bounded local single-subtest run passed: `test46/provider_burst` 12.38s, package 21.896s (raw log `/tmp/factory-c25-local-test46-provider-burst-raw.log`, 602 bytes, SHA-256 `1bf92f54005fa0eb4d32fea0288b409ade41d8267dc2978385ae4858f9fa695f`). The one full 12-subtest matrix also passed: `test46/provider_burst` 12.44s and package 29.362s (raw log `/tmp/factory-c25-local-test45-48-matrix-raw.log`, 5,046 bytes, SHA-256 `7460f44f141f64518282d7dbf90bb5d0a73dda88ef3e0f411727d7149d1023cf`). No focused child remained after either run.
- C20 retains exclusive production/fixture ownership of `session_tool_audio_remote_e2e_test.go` and live-session repair. C25 makes no edit outside its evidence folder. The exact next owner action is C20 to capture bounded server/snapshot phase timing on its same task; if its reviewed repair changes `main`, primary resumes C25 on the same task to refresh this source-only evidence and submit a changed head through script CI.

## Exact current-head refresh

- At current HEAD `209afd4ceca417fdca7964e3c0301ad4d3d1c250`, the bounded
  `verify.py --mode all` rerun completed in `10.263` seconds with `19`
  commands, decision `ACCEPTED`, no unexpected failure, clean source ancestry
  and audited-path diff checks, and `children_alive=false` for every child.
  The source-only diagnosis and all focused regressions remain unchanged; the
  generated `diagnosis.json`, `diagnosis.md`, and `provenance.json` now align
  with this exact pushed candidate head. The prior ff00c037, dab5c373, and
  3c2aea55 refreshes remain preserved as predecessor checkpoints.
- The known CI rejection remains historical and externally owned: C20 owns the
  `test46/provider_burst` remote snapshot phase. No C25 production or fixture
  repair is authorized, and no CI result is claimed from this local refresh.

## Current-main refresh

- The fetched reviewed main `98ce636dd67349ba64f22cd7916dd370cf4ba484` changed
  only C24 factory gate/docs paths relative to the C25 candidate; every audited
  production path is byte-identical to that main revision.
- At candidate `88f996c5384222d97b92578a6ba7f1ea83adbf2e`, `verify.py --mode all`
  completed in `9.475` seconds with 19 bounded commands. It returned
  `SOURCE_GAP_CONFIRMED`; source ancestry, production-path status and
  source-path equality were `ACCEPTED`; all focused tests/list/vet and the
  positive/negative controls were accepted. The intentional child-hang control
  timed out at 1 second, was terminated with native `-15`, and left no child.
- The C20 `test46/provider_burst` rejection remains the only unclaimed red check
  and is not fixed or waived by this documentation-only refresh. C25 remains
  source-only and retains no C20 production ownership.
- The final exact-head rerun at `672659326172756e26a228009560062d412c6650`
  completed in `9.680` seconds with 19 commands and the same accepted diagnosis,
  ancestry/path equality, focused controls, and clean child shutdown evidence.

## Review-78 repair checkpoint

- The latest C25 task-row rejection required exact-head evidence, fail-closed
  dependency/empty-test controls, lifecycle mixer-field capture, a reproducible
  source archive, fixture digests, a complete changed-path allowlist, and a
  corrected service-test reachability statement. The repair is confined to
  this admitted evidence folder; no production, shared-module, baseline, or
  existing fixture content was changed.
- At current source candidate `8e96dc077744d204994274b22356af8e856a8aa7`,
  `verify.py --mode all` completed in `16.275` seconds with `23` bounded
  commands and decision `ACCEPTED`. Source ancestry and audited-path equality
  returned 0, and clean shutdown held for every child.
- `git archive` rebuilt the pinned source at
  `98ce636dd67349ba64f22cd7916dd370cf4ba484` as 55,408,640 bytes with SHA-256
  `e77933bdc0f47a2f885581eb9b51a297b763f7a7b54bffe3b07254fa2126f11e`; the
  materialized `source.tar` matched the same digest.
- The committed fixture manifest is `e640b7052eb93966be24d3016fe60995156e45cf98e2d9144a3f60c1521da95f`.
  It accepts `allowed_room_graph.go` (`6833a559...ecb7b`, 769 bytes, 25
  lines) and `forbidden_room_graph.go` (`46c2512...760a`, 660 bytes, 18
  lines) exactly.
- The complete source-to-candidate and working-tree path set stayed under
  `docs/temp/projects/audio-runtime/audio-runtime-c25-audio-device-boundary-diagnosis/`;
  `outside_allowed=[]` and `malformed_paths=[]`. Positive canonical control
  was `ACCEPTED`; negative host-ticker/local-codec/direct-device control was
  `REJECTED_AS_FORBIDDEN`; zero-test discovery and child-hang cleanup controls
  were accepted as harness controls.
- The source oracle now requires the actual lifecycle field at
  `session_room_lifecycle.go:74`. It records that
  `servicetest/runtime.go:109-112` imports the legacy package for session
  helpers but exports no `RunRoom`; the legacy entrypoint is covered by its
  own internal package tests. The historical C20-owned hosted
  `test46/provider_burst` snapshot timeout remains unwaived and unchanged.
- After the repair checkpoint was committed at
  `9e61354c27ca381ed204ab05dec2aa7fd816c1d9`, the exact committed-head rerun
  completed in `10.503` seconds with `23` bounded commands. The source archive,
  fixture manifest, complete allowlist, dependency controls, package probes,
  focused causal/regression tests, race, vet, zero-test control, timeout
  cleanup, and clean shutdown remained accepted. The generated evidence now
  identifies that measured candidate; the final evidence-only successor does
  not alter the audited source paths.

## Review-86 causal repair checkpoint

- Repair commit `6151f4b7eede2a8b5a5fd9867c856a8743ddc412` replaces the
  fixture-text dependency check with a bounded Go AST oracle. It parses import
  declarations and selector calls, rejects the real forbidden fixture, rejects
  an allowed-fixture mutation containing `time.NewTicker`, and accepts a
  comment-only mutation containing the same forbidden tokens. Failed, empty,
  malformed, or incomplete oracle output fails closed.
- The public-route reachability fields are now derived from the inspected route
  source; the service-test export flag is derived from its actual declarations.
  The zero-test regression keeps its native `zero test discovery` assertion but
  gives cold-cache compilation the verifier's bounded `55s` child allowance
  within the `60s` contract.
- At this committed source candidate, `verify.py --mode all` returned
  `ACCEPTED` in `14.734s` with clean shutdown. The AST build/run, positive,
  negative, mutation, comment-only, stale/false-success/truncated, zero-test,
  child-hang, archive, fixture, ancestry/path-allowlist, package, focused
  normal/race/vet controls all passed. No production/shared/baseline/fixture
  path was modified; C20's hosted `test46/provider_burst` timeout remains
  separately owned historical evidence.

## Current-head coverage rejection

- Candidate `b061bf852076d4b36526600377ad916751e32b4b` was submitted on PR417.
- The complete latest coverage job was run `34413969081`, job
  `102674534021`, check `CI (coverage)`. Its 31,872-byte raw log has SHA-256
  `07a0eb388e05ca832306dfdcd0191d8c9a71ecc2c6c53e5528684a4f117a8c6e` and is
  recorded in `ci-diagnosis.json`; the raw job metadata is retained alongside
  it.
- `make coverage` failed in
  `agent-cli/internal/wire/composition_test.go:417` because the composed
  session error was `<nil>` instead of the expected RTC media-capability
  error after preflight. The package exited 1 and coverage exited 2; no
  second failure was present in that job log.
- The local focused causal control passed with coverage instrumentation in
  `0.563s` at `4.7%` package coverage. C25 therefore records an external
  composition failure, not a verifier failure or a C25 production defect.
- C20 retains the admitted composition/recording/live repair ownership; C21's
  remote fixture/gateway/duplex ownership is not implicated. C25 will not
  resubmit unchanged source. The next action is a reviewed C20 repair, then a
  C25 exact-head verifier refresh and same-task script-CI resubmission.

## Exact-head verifier refresh after rejection

- At measured candidate `a52291203d244d9d4f4d3a06511b9519ac65acc9`,
  `verify.py --mode all` returned `ACCEPTED` in `14.0s` across `25` bounded
  commands.
- Source/archive identity, AST dependency controls, fixture manifest, complete
  owned-path allowlist, focused normal/race/vet regressions, zero-test handling,
  child-hang cleanup and aggregate shutdown all remained accepted; no child
  survived.
- This refresh changes only owned evidence metadata. The latest hosted coverage
  rejection remains C20-owned at `b061bf85`; C25 does not claim a repair or
  resubmit unchanged implementation.
