# C25 evidence ledger

## Admission and ancestry

- Factory admission command: `python3 $FACTORY_ROOT/factory/scripts/project-control.py verify-work --type task --name audio-runtime-c25-audio-device-boundary-diagnosis`.
- Admission result: `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c25-audio-device-boundary-diagnosis"}`.
- PRD branch: `codex/audio-runtime-c25-audio-device-boundary-diagnosis`; isolated worktree and `git branch --show-current` agree.
- `origin/main` after fetch: `5d5afcb14d7b269378020809f5a2418c499ac94d`.
- Baseline ancestry: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` is an ancestor of the admitted head.
- Integration ancestry: `8bdafc7f947a3a2c9856220abdc539437035bd21` is an ancestor of the admitted head.
- Admitted source archive SHA-256: `0bebedbf449193683267220769084393f0c3e175d59d7e6dc7ba0992c06364ac`.
- The canonical board had task row `work-task-71` in `PROCESSING`, plan row `work-plan-70` complete, no C25 review row, and the task-row CI rejection for PR417 at `1040f92478bd1da6450f0f9a1fc1f4532ff34937`. C18/C21 review findings were read and remain outside this owned path.

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
| Public route | `agent-cli/internal/wire/wire_gen.go`; `agent-cli/internal/transport/cli/room.go` | 109-110; 52 | `yui room run` receives public `runtimeRooms.Service`, not legacy `RunRoom`. |

The verifier stores SHA-256 hashes and line counts for all audited files in `provenance.json`.

## Dependency and regression evidence

The refreshed `verify.py --mode all` passed at candidate `1040f92478bd1da6450f0f9a1fc1f4532ff34937` in 9.493 seconds:

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
