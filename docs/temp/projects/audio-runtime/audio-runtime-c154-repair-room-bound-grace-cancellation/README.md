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
- Pre-rejection candidate head: `6e80575de0e707da69bf9ab1f4e70668a66b579d` (the head named by the current CI feedback).
- Earlier integrated evidence checkpoint: `d116ce3e63ad8957678e556ab5cee682002cafb6`.
- Static-repair checkpoint: `6c91ba2` (`fix(room): preserve architecture gate metrics`).
- CI-repair checkpoints: `855188771` (`fix(room): preserve grace completion metadata`) and `2654c2bce` (`docs(room): explain bound grace ownership`).
- Final candidate head for push: `2654c2bce7d9816b1213e6df54c4de77dcf6b78b`.
- Both required ancestors are present; no reset or running-host checkout merge was used.

## Failing-before and causal finding

The accepted-main negative control was run in a temporary detached worktree and removed after the run. The exact C154 cover command exited failed: three duration subtests reported `active response cancellations = 0, want exactly one`, with final coverage `8.0%`. The exact normal and race commands also reproduced the instability: `Go test: 148 passed, 2 failed` and `Go test: 56 passed, 4 failed`, respectively. Earlier hosted evidence recorded the same C154 duration-zero failure in script CI run `34513957197`; that historical run is not treated as current-head CI evidence.

The cause was coordinator ownership loss during the unchanged 40 ms bound grace: an active response could retire from the active map before the force phase, so the force phase had no runtime through which to deliver the required cancellation. The repair snapshots runtimes at bound start and retains that snapshot through grace. The bound-start lifecycle mark remains authoritative; the force phase marks only bound cancellation on the retained runtimes and then performs the existing active-response cancellation. Grace duration, deadlines, provider-failure precedence and redaction assertions were not changed.

The bound test has a synchronized active-response observation, asserts zero cancellation in the bound callback, and asserts exactly one active cancellation and zero peer cancellation after completion. The duration fixture queues the provider handshake before connection readiness so the response is active during admission. A test-only no-tick cadence isolates this bound ownership oracle from an unrelated periodic mixer-silence admission race; the production mixer is still constructed and torn down, and accumulated healthy audio/tool tests retain their normal cadence. This isolation is not claimed as proof that periodic mixer ingress is causal to C154.

## Static CI repair

The canonical task feedback for PR `#520` head `e8196c4b6be1d19d65e65f6b63ee63cf7bd167cc` named `CI (static)` failure. The bounded local reproduction found the same 18 architecture findings: the two owned files drifted from their immutable baseline metrics, and the new nested callbacks exceeded the test/coordinator complexity budgets. No review findings were present. The repair keeps the existing baseline identities at their prior values, moves only the new test branching into named helpers, and uses the bound-start snapshot directly in the force phase; the shared baseline was not edited.

## Rejected head and causal repair

Canonical task feedback for PR `#520` at rejected head `6e80575de0e707da69bf9ab1f4e70668a66b579d` reported `CI (coverage)=FAILURE` ([log](https://github.com/portpowered/go-agent-harness/actions/runs/34790722251/job/103814302202)) and `CI (hermetic)=FAILURE` ([log](https://github.com/portpowered/go-agent-harness/actions/runs/34790722251/job/103814302190)). Both retained logs failed on the same pre-existing regression, `TestRunRoom_MaxTurnsDrainsResponseAlreadyInFlight`: the force phase re-marked a retained lifecycle after its response completed during grace, overwriting `max_turns_reached_mid_response` with `max_turns_reached` in the completion metadata. The repair removes only that redundant force-phase re-mark; bound-start ownership, bound cancellation and the existing cancellation path remain intact.

## Candidate verification

Targeted and accumulated commands below were run on final repair head `2654c2bce7d9816b1213e6df54c4de77dcf6b78b`, with no retry-until-green loop. The full-package confirmation immediately before the comment-only checkpoint is identified explicitly:

- C154-01 normal, count 50: pass, `23.224s`.
- C154-01 CGO-disabled `nomicrophone` coverpkg, count 50: pass, `22.709s`, coverage `7.9%`.
- C154-01 race, count 20: pass, `11.773s`.
- C154-02 normal, count 50: pass, `33.821s`.
- C154-02 CGO-disabled `nomicrophone` coverpkg, count 20: pass, `18.947s`, coverage `8.1%`.
- C154-02 race, count 20: pass, `18.694s`.
- Exact rejected regression `TestRunRoom_MaxTurnsDrainsResponseAlreadyInFlight`: normal count 50, CGO-disabled count 50 and race count 20 all pass (`5.604s`, `5.596s`, `3.943s`).
- Accumulated room bound/audio/tool/lifecycle/participant/terminal and `TrackedSession` normal, count 3: pass, `38.816s`.
- Accumulated race, count 1: pass, `16.775s`.
- Accumulated CGO-disabled `nomicrophone` coverpkg, count 1: pass, `17.244s`, coverage `11.6%`.
- Full `agentruntime` package normal and CGO-disabled `nomicrophone` coverpkg runs immediately before the comment-only checkpoint: pass (`70.042s`; `71.867s`, coverage `18.9%`).
- `make vet` across all 15 modules: no issues.
- Post-repair `make architecture-size-check`: pass (`202` packages, `1,941` files, `28,792` functions); the coordinator is back to its baseline `893` physical lines and the preserved bound test function/literal metric identities are clean.
- `make test-architecture-gate`: pass (`tools/architecturegate`, `3.452s`).
- Post-repair `make fmt`, `make wire-check`, pinned `make staticcheck` (`2026.1`) and pinned `make lint` (`v2.9.0`, 0 issues in all 15 modules): pass.
- `git diff --check`: clean.

The source diff against fetched `origin/main` is limited to the two admitted Go paths: `session_room_coordinator.go` and `session_room_bound_shutdown_test.go`. Generated coverage profiles are not part of the handoff.

## Handoff boundary

This is a C154 baseline causal repair. It is not C143 recovery, vertical acceptance or whole-project completion; AUDIO, DEVICE, SERVICE, TRACE, REPLAY, FAILURES, QUALITY and PARITY remain open, as do the later C154 vertical probe and changed-head C143 recovery. Script CI owns broad CI execution and polling for the exact pushed head. No CI-green, independent-review or merge result is claimed here; those gates remain next.
