# C103 pre-edit census

Recorded before implementation mutation on 2026-09-12.

## Admission and ancestry

- Project: `audio-runtime`
- Contract: `audio-runtime-v1`
- Work: `audio-runtime-c103-retire-cli-rtc-session-runtime`
- Task identity: `work-task-200`, canonical state `init`
- Admission: `factory/scripts/project-control.py verify-work --type task --name audio-runtime-c103-retire-cli-rtc-session-runtime` returned `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c103-retire-cli-rtc-session-runtime"}`.
- Branch and PRD: `codex/audio-runtime-c103-retire-cli-rtc-session-runtime`
- Isolated worktree: `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c103-retire-cli-rtc-session-runtime`
- Pre-edit HEAD and fetched `origin/main`: `d4766c3dbbf2c198142047ead4449d58dd47d485`
- Required startup integration ancestor: `8bdafc7f947a3a2c9856220abdc539437035bd21`
- Required original baseline ancestor: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`
- The isolated worktree was clean and `git diff --check` passed.

## Immutable owned baseline

`agent-cli/internal/services/internal/agentruntime/session_runtime_rtc.go`

- Physical lines at accepted main: `624`
- SHA-256: `7f881c42da476b6d072ac91ec0135814754092856daa1a6c41643903552d9abc`
- Runtime/lifecycle symbols: `SessionRTCRuntime`, `SessionRTCDataPlane`, `SessionRTCRuntimeFactory`, `SessionRTCSignalingResolver`, `SessionRTCDataPlaneFactory`, `SessionRTCMediaSourceOpener`, `SessionRTCComponents`, `NewSessionRTCRuntimeFactory`, `NewSessionRTCRuntimeFactoryWithObservability`, `validateSessionRTCComponents`, `planWebRTCSessionRuntime`, `SessionRTCRuntimeError`, `wrapSessionRTCRuntimeError`, `sessionComposedRTCRuntime`, `closeSessionRTCResources`, `closeSessionRTCResource`, `sessionRTCRuntimeInferencer`, `sessionRTCRuntimeSession`, and `sessionRTCLazyDialer`.
- Direct planning/composition callers are retained at the CLI edge in `session_runtime_plan.go`, `service.go`, `agent-cli/internal/services/wire/rtc.go`, `agent-cli/internal/wire/wire.go`, `agent-cli/internal/wire/rtc_runtime.go`, `agent-cli/internal/wire/composition.go`, and their existing tests. The provider/config/record-path selection logic in `planWebRTCSessionRuntime` remains in this file's adapter after extraction.

## Protected peer/source hashes

These were read-only during the census and must remain unchanged by C103:

| Path | Lines | SHA-256 |
| --- | ---: | --- |
| `agent-cli/internal/services/internal/agentruntime/rtc_device_binding.go` | 303 | `0c0a72c306f748535d8d35ff2c019d1ac1c8408b7f22dff46814e2abf0a57bc1` |
| `agent-cli/internal/services/internal/agentruntime/session_runtime_openai.go` | 259 | `499e9c92856e94192d8e462619deeb4d15246190d2b14effa09502c21ddb8198` |
| `agent-cli/internal/services/internal/agentruntime/session_runtime_grok.go` | 124 | `864fa8b8a3f889d883733a4c1b202e0630d0155ce34f03f32beb712085f788d5` |
| `agent-cli/internal/services/internal/agentruntime/session_room_run.go` | 1192 | `2ac0ff50ee59a1ec8f04682e04d876d270146ecda8245c5d14d28a5f7120e8d6` |
| `agent-cli/internal/services/internal/agentruntime/session_diagnostics.go` | 398 | `0584e3acc16e29b8fb79ece8a19199f0f8690de39099af922f08781edd957eb8` |
| `agent-cli/internal/services/internal/agentruntime/session_diagnostics_response.go` | 989 | `6d9b24f1e134737f1187c1e4b76390a588be200c2c560aff5e99dbff6a3d2f54` |
| `agent-cli/internal/services/internal/agentruntime/session_runtime_plan.go` | 727 | `dd9c598e470816f4f5ec18c97059c55a23b04a0edcf39c94374433db1f70d33b` |
| `agent-cli/internal/services/internal/agentruntime/service.go` | 338 | `d3a1650dfb0a50b8cfcdf9376422d1e054f747a00e4b1951753cff6f9fca82b0` |
| `scripts/wire-packages.txt` | 9 | `3958443ed08ad6a550fd29ce8191c026d1a4017339e00c0b7406e0dcec7de05b` |
| `docs/architecture/architecture-size-baseline.json` | 35039 | `6d61448b83eca374b324335f6a9e2fac768969d073b1091d17d48d45bf3badd1` |

## Ownership constraints

- C79 currently owns both shared registry files; no C103 edit is authorized until its reviewed guarded merge explicitly releases them.
- C96 owns `rtc_device_binding.go`; C101 owns the provider runtime files; C93 owns the room media slice; C79 owns diagnostics; `session_runtime_plan.go` and `service.go` remain read-only for C103.
- No prior C103 review or CI rejection row exists on the canonical board. The repository `progress.txt` contains older C07/C18 checkpoints and no C103/RTC entry; it is retained as historical context, not used as an acceptance waiver.

## Implementation checkpoint

- `session_runtime_rtc.go` is now 264 lines and retains only provider/config/record-path planning, `sessionRuntimePlan` adaptation, and explicitly deprecated CLI compatibility adapters.
- New public contract, private implementation, dedicated Wire graph, tests, coverage manifests, external consumer, mutation verifier, and bounded runner are under the admitted C103 owned paths.
- Normal focused service/CLI tests passed (`1084` cases across four packages); the isolated C103 race matrix passed `60` CLI cases across three repetitions and `45` service cases across three repetitions. Coverage is `85.7%` public contract, `81.2%` private service, and `100.0%` Wire.
- `verify.py --mode positive-and-two-mutations` passed its positive control and both compiling causal mutants failed their intended cleanup-order/typed-error oracles. `verify.py --mode retirement-and-owned-paths` verified the 263-line cap, immutable baseline, and all 10 protected hashes.
- External consumer passed under `GOWORK=off`; bounded runner passed public RTC hermetic success/failure-identity tests and the shipped-yui credential-free replay. Repository `make architecture-size-check` now reports only the nine shared C79 baseline-stale entries plus generated-file registration; no C103-owned architecture issue remains. Repository `make wire-check` is currently blocked only by the unregistered C103 generated graph because `scripts/wire-packages.txt` remains under C79 ownership.
- The broad CLI race regex selected the peer C64 device-drain test and reported two exact-sample failures; the same test passed isolated under `-race -count=1`, and the narrower C103 matrix passed without that peer case. No peer-owned file was changed.

## Concurrent-close repair checkpoint

- Revision `f5162a25ee4875475c37945faa90fb5fc2c71c42` retains cleanup errors from an in-progress `Start` when concurrent `Close` cancels the start; typed-nil component returns are also treated as unavailable. Added deterministic normal/race coverage for both causal paths.
- Focused service tests passed normally and under `-race` (`75` cases in the service package at `-count=5`); CLI C103 normal and race matrices passed (`42` cases each at `-count=3`).
- Accumulated `COUNT=3 scripts/test-session-ci-regressions.sh all` passed with exit `0` across normal, coverage, and race modes, including the expected negative-control rejections.
- Rebuilt the shipped artifact from this revision: `artifacts/yui` SHA-256 `664de5ef35aecbfa156613be172663788c2757afffcb0400a38a9cc2d91879fd`; the bounded runner returned `ACCEPTED` for both RTC cases and the shipped-yui replay.
- Admission was reverified as admitted, the PRD branch still matches the isolated worktree, and local HEAD equals the pushed branch. C79 task `work-task-114` remains `in-review` and still owns the shared Wire registry and architecture baseline; those files remain unchanged.

## Exact CI quality repairs

- PR #490's exact prior CI run `34707823382` at `f5162a25` completed with all checks except `CI (static)` successful. Its completed log identified the C103-owned defects: `go-agent-runtime/services/rtcsession/internal/service/service_test.go` at 606 physical lines (budget 600) and an unchecked `wrapper.Close` in `agent-cli/internal/services/internal/agentruntime/session_runtime_rtc_test.go:327`. The generated-Wire registration and eight stale C103 baseline entries were separately identified as C79-owned and remain untouched.
- This checkpoint removes six non-semantic blank lines to bring `service_test.go` to the 600-line budget and checks the wrapper cleanup error with `t.Errorf`. No assertion, timeout, baseline, registry or peer-owned path was weakened or changed. PR #490 has no independent review rows or review findings yet; the current-head CI run was still in progress when this repair began.
- After those first repairs, the pinned local lint gate reached the new service and reported nine additional C103-owned findings: context propagation at the lifecycle observation boundary, diagnostic-only observer error handling, typed fixture assertion checking, exhaustive nil-resource classification, and the repeated nil-error test literal. The service now propagates the start context without cancellation for post-cleanup observation, documents deliberately diagnostic-only observer errors, checks the fixture assertion, documents the intentional non-nilable reflection default, and centralizes the nil-error text. Pinned `make lint` subsequently reports `0 issues`; the focused normal/race matrix reports `225` passing cases in four packages.
- `make architecture-size-check` now reports only the eight demonstrated stale C103 entries in the C79-owned architecture baseline plus the generated-file spoof for the C103 graph, and `make wire-check` reports only the same unregistered C103 generated graph in the C79-owned Wire registry. No shared file was edited.

## Current-head static rejection and retained dependency

- Fresh fetch confirms `origin/main` remains `d4766c3dbbf2c198142047ead4449d58dd47d485`; C103 HEAD is `1711bf259e4ae5d5bdf7a636116e01fa2d8ad983`, pushed on PR `#490`. Admission remains admitted and `prd.json.branchName` matches the isolated worktree.
- PR `#490` run `34709821266`, static job `103596417418`, completed with the same two shared-file findings: `make wire-check` reports the unregistered `go-agent-runtime/services/rtcsession/wire/wire_gen.go`; `make architecture-size-check` reports exactly nine issues, consisting of the eight stale C103 `session_runtime_rtc.go` baseline entries and that generated-file spoof. The complete job log was read from the GitHub job-log endpoint; no other static findings were present.
- The owned service normal/race matrices passed `45/45` each, the focused CLI normal/race matrices passed `93/93` and `72/72`, the positive/two-mutant verifier passed with both mutants failing their intended oracles, the retirement/protected-hash verifier passed, the external `GOWORK=off` consumer passed, and `COUNT=3 scripts/test-session-ci-regressions.sh all` passed in normal, coverage and race modes.
- Canonical board state still shows C79 `work-task-114` at `ci-pending` with no guarded merge/release. C79 retains exclusive ownership of `scripts/wire-packages.txt` and `docs/architecture/architecture-size-baseline.json`; C103 makes no edit to either shared file and does not resubmit this known-failing head.
- Next action: after C79 records its reviewed guarded merge and explicit shared-file release, fetch/integrate the then-current `origin/main`, apply only the demonstrated C103 Wire registration and eight downward/stale baseline deletions, rerun bounded gates, commit/push the changed PR `#490`, and submit that changed head to script CI without polling.
