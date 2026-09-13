# C154 bound-grace cancellation handoff evidence

## Identity and ancestry

- Project/contract: `audio-runtime` / `audio-runtime-v1`.
- Factory session/server: `~default` / `http://127.0.0.1:7439`.
- Admission command: `python3 "$FACTORY_ROOT/factory/scripts/project-control.py" verify-work --type task --name audio-runtime-c154-repair-room-bound-grace-cancellation --root "$FACTORY_ROOT"`.
- Admission result: `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c154-repair-room-bound-grace-cancellation"}`.
- Branch: `codex/audio-runtime-c154-repair-room-bound-grace-cancellation`, matching `prd.json.branchName`.
- Planning main: `4a1c399ccbb3d780be95eb04316e84b8f11a6646` (the accepted-main baseline named by the admitted PRD).
- Startup integration ancestor: `8bdafc7f947a3a2c9856220abdc539437035bd21`.
- Review-time `origin/main`, fetched before final verification: `2c79ec6a931c3e86944d6a625d0a5060b4f85aa0`.
- Tested integrated candidate head before this evidence-only commit: `d116ce3e63ad8957678e556ab5cee682002cafb6`.
- Both required ancestors are present; no reset or running-host checkout merge was used.

## Failing-before and causal finding

The accepted-main negative control was run in a temporary detached worktree and removed after the run. The exact C154 cover command exited failed: three duration subtests reported `active response cancellations = 0, want exactly one`, with final coverage `8.0%`. The exact normal and race commands also reproduced the instability: `Go test: 148 passed, 2 failed` and `Go test: 56 passed, 4 failed`, respectively. Earlier hosted evidence recorded the same C154 duration-zero failure in script CI run `34513957197`; that historical run is not treated as current-head CI evidence.

The cause was coordinator ownership loss during the unchanged 40 ms bound grace: an active response could retire from the active map before the force phase, so the force phase had no runtime through which to deliver the required cancellation. The repair snapshots runtimes at bound start, retains that snapshot through grace, re-marks each retained lifecycle for coordinator stopping and bound cancellation at force, and then performs the existing active-response cancellation. Grace duration, deadlines, provider-failure precedence and redaction assertions were not changed.

The bound test has a synchronized active-response observation, asserts zero cancellation in the bound callback, and asserts exactly one active cancellation and zero peer cancellation after completion. The duration fixture queues the provider handshake before connection readiness so the response is active during admission. A test-only no-tick cadence isolates this bound ownership oracle from an unrelated periodic mixer-silence admission race; the production mixer is still constructed and torn down, and accumulated healthy audio/tool tests retain their normal cadence. This isolation is not claimed as proof that periodic mixer ingress is causal to C154.

## Candidate verification

All commands below were run on the integrated candidate head above, with no retry-until-green loop:

- C154-01 normal, count 50: `Go test: 150 passed in 1 packages`.
- C154-01 CGO-disabled `nomicrophone` coverpkg, count 50: `ok ... 23.989s coverage: 7.9%`.
- C154-01 race, count 20: `Go test: 60 passed in 1 packages`.
- C154-02 normal, count 50: `Go test: 200 passed in 1 packages`.
- C154-02 CGO-disabled `nomicrophone` coverpkg, count 20: `ok ... 14.228s coverage: 8.1%`.
- C154-02 race, count 20: `Go test: 80 passed in 1 packages`.
- Accumulated room bound/audio/tool/lifecycle/participant/terminal and `TrackedSession` normal, count 3: `Go test: 306 passed in 1 packages`.
- Accumulated race, count 1: `Go test: 102 passed in 1 packages`.
- Accumulated CGO-disabled `nomicrophone` coverpkg, count 1: `ok ... 13.487s coverage: 11.6%`.
- `go vet ./agent-cli/internal/services/internal/agentruntime`: no issues.
- `git diff --check`: clean.

The source diff against fetched `origin/main` is limited to the two admitted Go paths: `session_room_coordinator.go` and `session_room_bound_shutdown_test.go`. Generated coverage profiles are not part of the handoff.

## Handoff boundary

This is a C154 baseline causal repair. It is not C143 recovery, vertical acceptance or whole-project completion; AUDIO, DEVICE, SERVICE, TRACE, REPLAY, FAILURES, QUALITY and PARITY remain open, as do the later C154 vertical probe and changed-head C143 recovery. Script CI owns broad CI execution and polling for the exact pushed head. No CI-green, independent-review or merge result is claimed here; those gates remain next.
