# C85 verification ledger

Project: `audio-runtime`
Task: `audio-runtime-c85-retire-cli-room-replay-scheduler`
Implementation checkpoint: `b07e8e6`
Baseline: `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`
Branch: `codex/audio-runtime-c85-retire-cli-room-replay-scheduler`

## Admission and scope

- `project-control.py verify-work --type task --name audio-runtime-c85-retire-cli-room-replay-scheduler` returned `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c85-retire-cli-room-replay-scheduler"}`.
- `prd.branchName` matches the isolated worktree branch.
- `git fetch origin main` completed; `HEAD`, `origin/main`, and the planning-main checkpoint are descendants of the admitted startup ancestry.
- `verify.py --mode all` passed: baseline scheduler `409` lines, current adapter `139` lines, `270` retired; no excluded paths changed; external imports are only the public runtime service and Wire package.

## Focused causal and regression evidence

- `rtk go test ./services/roomreplayschedule/...`: `12 passed in 3 packages`.
- CLI fractional-timeline and production-mixer overlap tests: `2 passed in 1 package`.
- Accumulated replay regression selection: `38 passed in 1 package`.
- Runtime race selection (`Test(Build|Run)`, count `3`): `36 passed in 3 packages`.
- Production-mixer replay race: `1 passed in 1 package`.
- Independent credential-free consumer: `{"external_consumer":true,"credential_free":true,"released":2,"acknowledgements":1,"malformed_rejected":true}`.
- Coverage gate against the current local profiles: `coverage gate passed: 179 registered packages checked across 7 profiles`.
- `make coverage-registration`: `179 workspace packages checked across 6 modules`.
- `make lint`: all workspace modules reported `0 issues`.
- `make staticcheck` and `make vet`: completed successfully for all configured workspace modules.

## Shared C79 gates intentionally deferred

The task does not modify shared registry or architecture-baseline files. These exact gate results remain open for the C79 owner/released-main reconciliation:

- `rtk make wire-check` exited `2`: `wire-check: Wire registry mismatch: unregistered=['go-agent-runtime/services/roomreplayschedule/wire/wire_gen.go'], outside modules=[]`.
- `rtk make architecture-size-check` exited `2` with `10` findings: `9` stale baseline entries for the retired legacy scheduler file/functions, plus `generated-file-spoof services/roomreplayschedule/wire/wire_gen.go: generated header is not registered with a reproducible generator`.

Required shared files remain unchanged: `scripts/wire-packages.txt` and `docs/architecture/architecture-size-baseline.json`. No new architecture complexity, size, or mutable-global findings were reported.

CI has not been polled or represented as green. The exact next action is to push this candidate and submit it to the script CI gate; repair only exact CI rejection findings on this same task/branch.

## Accepted-main reconciliation and current-head evidence

- Released accepted main advanced to `59af6325614d80173447fe2018a0471e27b4e7b1` for the C64 predecessor. It was integrated in this isolated worktree with merge commit `808d8ec5`; the original C85 commits, startup ancestry, and fixed planning baseline remain preserved. The accepted C64 paths remain predecessor changes and were not edited by C85.
- The causal repair is `c57fd5e5`: the extracted service rechecks target activity before every source-to-target frame contribution, preserving the legacy mid-frame inactive-target error barrier. The repair has normal and race causal coverage.
- `verify.py --mode all` passes on the merged candidate: fixed baseline `409` lines, retained adapter `139` lines, `270` retired, released-main scope base `59af6325`, no excluded paths, and only the public scheduler and Wire packages imported by the external consumer. The scope verifier was updated in `e6b19291` to compare candidate ownership against released `origin/main` while retaining the fixed retirement baseline.
- Post-merge focused service tests pass at `-count=3` in normal and race modes; fractional-timeline/production-mixer scheduler tests pass at `-count=3` in normal and race modes.
- Post-merge accumulated replay regressions pass at `COUNT=3` in both normal and race modes. The normal integration matrix passed in `82.880s`; the race integration matrix passed in `226.969s`.
- The post-merge credential-free external consumer reports `{"external_consumer":true,"credential_free":true,"released":2,"acknowledgements":1,"malformed_rejected":true}`.
- Post-merge gates pass: `make coverage-changed COVERAGE_BASE=84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`, `make coverage-registration`, `make vet`, `make staticcheck`, `make lint`, and `git diff --check`.
- `make wire-check` remains blocked only by the unregistered `go-agent-runtime/services/roomreplayschedule/wire/wire_gen.go`; `make architecture-size-check` remains blocked by the nine stale legacy-scheduler baseline entries plus the generated-file-spoof finding. The required shared files `scripts/wire-packages.txt` and `docs/architecture/architecture-size-baseline.json` remain unchanged under active C79 ownership.
- PR #477 is still the same open candidate. CI has not been polled or represented as green; after C79 releases its guarded shared-file repair, fetch/reconcile accepted main again, apply only the generated-Wire registration and nine demonstrated downward baseline removals, rerun the bounded gates, push the same branch, and submit the same task to script CI.

## CI rejection reconciliation

- Completed script-CI run `34686754247`, job `103534962321`, rejected PR #477 at head `f22179dc729cbb24264194ebb6595769341dc657` in `CI (static)`. The full job log was inspected once after the board rejection; no CI polling was performed.
- The exact job failures are the two previously deferred shared gates. `make wire-check` reports only `unregistered=['go-agent-runtime/services/roomreplayschedule/wire/wire_gen.go']`. `make architecture-size-check` reports exactly ten findings: nine stale entries for the retired scheduler file/functions (`file-lines`, `cognitive-complexity`, `cyclomatic-complexity`, `function-lines`, and `function-statements` across `roomReplaySchedule.run` and `newRoomReplaySchedule`) and one `generated-file-spoof` for the same generated Wire file.
- The same completed log shows `make fmt`, `make vet`, pinned golangci-lint `v2.9.0` with `0 issues` in every module, and pinned staticcheck `2026.1` completing successfully. No C85 production, test, peer, or evidence defect was reported by this rejection.
- C79 still exclusively owns `scripts/wire-packages.txt` and `docs/architecture/architecture-size-baseline.json`; its canonical task remains processing and its latest review is failed with actionable repairs. Therefore C85 cannot legally apply the two required shared-file changes or resubmit this head. Preserve this negative evidence and retain the same task through `CONTINUE`.
- Exact next action after C79's reviewed guarded merge releases both leases: fetch `origin/main`, reconcile accepted main, add only the C85 generated-Wire registration and the nine demonstrated downward baseline deletions, rerun `make wire-check`, `make architecture-size-check`, the C85 verifier and accumulated focused regressions, then commit/push PR #477 and submit the changed same task to script CI without polling.
