# C18 strict replay extraction inventory

This checkpoint records the admitted C18 extraction boundary. It preserves the
current public admission/planning service and moves only the complete strict
Prepare/Run/ValidateComplete workflow into the runtime service.

## Provenance and scope

- Work: `audio-runtime-c18-public-strict-replay`
- Project/session: `audio-runtime` / `~default`
- Branch: `codex/audio-runtime-c18-public-strict-replay`
- Initial C18 planning main: `3bbd052b66d2bf67adfc5b9348fb4b4ad8da3c84`
- Current fetched `origin/main`: `e4137eba6a6499142f50701609c1149afc71db84`, integrated by merge `87f33e51`
- Required startup integration ancestor: `8bdafc7f947a3a2c9856220abdc539437035bd21`
- Original refactor baseline: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`
- C14 accepted audit source: `f98733864d9c46c691a1198d714943afbd52fbcb`
- Admission: `project-control.py verify-work --type task --name audio-runtime-c18-public-strict-replay` returned `admitted`.

C18 is a behavior-preserving SERVICE/EMBED extraction. `StrictEvidenceScope`
keeps recorded PCM availability distinct from production packet consumption;
the strict runtime consumes provider-wire `input_audio_buffer.append` packets,
not `Prepared.Audio`. The per-preparation deterministic clock is now passed to
AgentLoop through `agentloop.WithClock`; this is a runtime-timing repair inside
the extracted workflow, not a claim of full AUDIO/TRACE/DEVICE completion.

## Symbol and file map

| Existing owner | C18 owner | Boundary |
| --- | --- | --- |
| `agent-cli/internal/services/replay/interface.go` | `go-agent-runtime/services/replay/service.go` | Public `StrictRequest`, `StrictPrepared`, `StrictResult`, `StrictEvidenceScope`, runtime/factory and `StrictService` contracts. CLI file is aliases only. |
| `agent-cli/internal/services/internal/replay/service.go` | `go-agent-runtime/services/replay/internal/strict/service.go` | Bundle admission handoff, timeline origin, evidence projection, strict `Prepare`, `Run`, and completion validation. |
| `agent-cli/internal/services/internal/replay/recording_directory.go` | `go-agent-runtime/services/replay/internal/strict/recording_directory.go` | Manifest Lstat boundary, canonical `CaptureAdmission`, trace-directory resolution, manifestless trace compatibility. |
| `agent-cli/internal/services/internal/replay/runtime.go` | `go-agent-runtime/services/replay/internal/strict/runtime.go` | Offline OpenAI gateway/AgentLoop, captured packet input, exact initial update, terminal drain, and clock injection. |
| `agent-cli/internal/services/internal/replay/media.go` | `go-agent-runtime/services/replay/internal/strict/media.go` | Headless inbound-media drain and virtual playback interruption controller. |
| Strict unit/directory/media tests in CLI internal replay | `go-agent-runtime/services/replay/internal/strict/*_test.go` | Migrated behavioral controls remain next to the private implementation. |
| `agent-cli/internal/services/wire/replay.go` | Thin CLI adapter | Delegates to runtime replay Wire; no strict business construction. |
| `go-agent-runtime/services/replay/wire/{wire.go,wire_gen.go}` | Runtime Wire | Owns `NewService` admission and generated `NewStrictService`; binds `plan.Service` to `CaptureAdmission`. |
| `agent-cli/internal/wire/{wire.go,wire_gen.go}` | CLI application Wire | Keeps routing/presentation and delegates strict command construction through the thin adapter. |
| `agent-cli/internal/services/internal/agentruntime/replay_integration_test.go` | Existing CLI integration location | Uses public runtime replay Wire; its source-side recorder/provider harness remains CLI-owned test setup. |
| absent external consumer | `tests/embedding/{tools_test.go,cmd/strict-replay/,testdata/replay/}` | GOWORK=off public runtime consumer, positive exact tool/audio-input replay, no-device/no-executable-tool proof, and incomplete-bundle negative. |

## Import and Wire graph

```text
runtime replay public contracts
  <- replay/internal/strict
       <- replay/internal/plan (CaptureAdmission implementation)
       <- go-agent-loop / go-audio / go-llm-gateway
  <- replay/wire (generated NewStrictService)
       plan.New -> CaptureAdmission
       NewReplayClockFactory -> strict.ClockFactory
       strict.NewOpenAIRuntimeFactory -> StrictRuntimeFactory
       strict.New -> replay.StrictService
  <- agent-cli/internal/services/wire (thin adapter)
  <- agent-cli/internal/wire -> CLI presentation command
  <- tests/embedding (public runtime contracts and replay Wire only)
```

`strict` never imports `replay/wire` and never imports `agent-cli`; the injected
admission interface prevents a Wire cycle and reuses the canonical manifest,
path, digest, and confinement validator. Provider-only/live/passive routes stay
on their existing session/provider graphs.

## Test migration/accounting

- `service_test.go`: Prepare, exact-once tool result, model/handshake, clock
  independence, completion, outbound mismatch, audio evidence, and relative
  bundle controls.
- `recording_directory_test.go`: declared PCM/digest admission, root manifest
  symlink/nonregular rejection, and manifestless trace compatibility.
- `runtime_test.go`: headless media interruption boundary.
- `replay_integration_test.go`: source-side recorded-wire harness plus public
  runtime Wire strict replay; retained in CLI integration because it exercises
  CLI-owned trace observation setup.
- `tests/embedding/tools_test.go`: public Wire end-to-end result/output,
  recorded provider-wire PCM scope, deterministic prepared clock, missing
  timeline, missing recorded tool result, missing terminal response boundary,
  completion-forgery, nil-context and no executable-tool marker.
- `tests/embedding/cmd/strict-replay`: independently buildable consumer with
  no CLI imports, flags, terminal state, credential lookup, or device setup.
- `docs/temp/projects/audio-runtime/audio-runtime-c18-public-strict-replay/verify-public.py`:
  bounded external, strict/provider, interruption and local-SSE ask record/replay
  runner; it records literal child argv/cwd/raw output/status and fixture hashes.

Required route residuals remain explicit: provider-only replay still owns its
cwd/allow-path and executable-tool semantics; undeclared provider timeline/WAV
artifacts retain their route-specific behavior; file/virtual replay does not
prove physical device consumption or acoustic output; broader AUDIO/DEVICE,
TRACE, FAILURES, and PARITY gates remain open.
