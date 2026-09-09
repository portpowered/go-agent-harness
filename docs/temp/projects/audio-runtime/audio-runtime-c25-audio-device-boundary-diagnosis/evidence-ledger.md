# C25 evidence ledger

## Admission and ancestry

- Factory admission command: `python3 $FACTORY_ROOT/factory/scripts/project-control.py verify-work --type task --name audio-runtime-c25-audio-device-boundary-diagnosis`.
- Admission result: `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c25-audio-device-boundary-diagnosis"}`.
- PRD branch: `codex/audio-runtime-c25-audio-device-boundary-diagnosis`; isolated worktree and `git branch --show-current` agree.
- `origin/main` after fetch: `5d5afcb14d7b269378020809f5a2418c499ac94d`.
- Baseline ancestry: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` is an ancestor of the admitted head.
- Integration ancestry: `8bdafc7f947a3a2c9856220abdc539437035bd21` is an ancestor of the admitted head.
- Admitted source archive SHA-256: `0bebedbf449193683267220769084393f0c3e175d59d7e6dc7ba0992c06364ac`.
- The canonical board had task row `work-task-71` in `PROCESSING`, plan row `work-plan-70` complete, and no C25 review/rejection row. C18/C21 review findings were read and remain outside this owned path.

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
| Canonical timing/buffer graph | `go-audio/pkg/mixer/mixer.go`, `go-audio/pkg/mixer/input.go` | 15, 45, 58; 28, 80 | Injected `clock.TimerSource`, `audio.PCMFrame`, and `audio.NewFrameBuffer`. |
| Canonical room boundary | `go-agent-runtime/services/rooms/contract.go` | 95, 118; `.../lifecycle/graph.go` 66, 124 | Room owns lifecycle while media ports and mixer own media behavior. |
| Device boundary | `go-device-gateway/pkg/runtime/rtc_device_sink.go` | 40, 82, 90, 196, 201 | Device runtime owns playback queue/callback and consumption distinction. |
| Public route | `agent-cli/internal/wire/wire_gen.go`; `agent-cli/internal/transport/cli/room.go` | 109-110; 52 | `yui room run` receives public `runtimeRooms.Service`, not legacy `RunRoom`. |

The verifier stores SHA-256 hashes and line counts for all audited files in `provenance.json`.

## Dependency and regression evidence

`verify.py --mode all` passed:

- positive canonical dependency control: `ACCEPTED`;
- negative host-ticker/local-codec/direct-gateway control: `REJECTED_AS_FORBIDDEN`;
- `go list` for legacy room, legacy agentruntime, public CLI, canonical mixer, and public rooms packages;
- `go test ./pkg/mixer -run '^Test(Mixer|PCMAccumulator)' -count=1 -timeout=45s`;
- `go test ./pkg/agentloop -run '^TestProductionAudioAndDeviceOwnership$' -count=1 -timeout=45s`;
- `go test ./internal/room -run '^TestPCM16MixerMixesEveryActiveInputAndClips$' -count=1 -timeout=45s`;
- `go test ./internal/services/internal/agentruntime -run '^$' -count=1 -timeout=45s`.

All returned exit code 0; the aggregate was 3.552 seconds, every child stayed below 55 seconds, and shutdown was clean. This is source/package evidence only; it is not a physical-device or acoustic PASS.
