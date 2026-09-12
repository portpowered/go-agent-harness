# C104 pre-mutation census

Captured before implementation changes on 2026-09-12 in the isolated worktree
`/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c104-retire-cli-session-finalization-boundary`.

- Project admission: `project-control.py verify-work --type task --name audio-runtime-c104-retire-cli-session-finalization-boundary --root "$FACTORY_ROOT"` returned `status=admitted`, project `audio-runtime`, task name exact.
- Manifest: `audio-runtime-v1`, baseline `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`; startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21`; authority manifest/source-plan/request/acceptance are the admitted project files.
- Branch: `codex/audio-runtime-c104-retire-cli-session-finalization-boundary`, exactly equal to `prd.json.branchName`.
- Fresh main: `git fetch origin main` completed; `HEAD` and `origin/main` were `d4766c3dbbf2c198142047ead4449d58dd47d485`. Startup, planned baseline, and `origin/main` all passed `git merge-base --is-ancestor` checks. Worktree was clean and `git diff --check` passed.
- No C104 review attempt or CI rejection row exists on the canonical `~default` board. The task is `work-task-202` in `init`; the completed plan is `work-plan-201`.

## Immutable owned-source baseline

All counts and hashes below were read from the accepted current main before this
census file was added and are not candidate-derived.

| source | lines | SHA-256 |
| --- | ---: | --- |
| `agent-cli/internal/services/internal/agentruntime/session_finalizer.go` | 128 | `8d4afe893c64c54be15b435a9292ff511ea77592ad1e71c1b944eee538150236` |
| `agent-cli/internal/services/internal/agentruntime/session_termination.go` | 107 | `9dc18e2f9a5dc503f97089feb6fad145273aac654673b1a3efd46879b9c9af26` |
| `agent-cli/internal/services/internal/agentruntime/session_cancellation.go` | 84 | `bdb7e50876bd8d9b67fe04a736da9a91ce3ea2bbd10024ebf27d0f6bbbcd9edd` |

Total legacy production baseline: 319 lines. The named symbols and direct
production callers were inventoried with `rg`:

- Finalizer: `newSessionRuntimeFinalizer` is constructed by
  `session_runtime_plan.go`; `finish` is the deferred post-loop boundary; the
  `setDeviceBinding` and `cleanup` methods are local compatibility seams.
  `invokeSessionFinalizer` is also called by
  `session_duration_artifacts.go` for duration artifact cleanup.
- Termination: `sessionTerminationBoundary` is constructed by
  `session_live.go` and `session_duration_loop.go`; the local drain policy and
  errors are consumed by `session_drain.go` and the owned termination tests.
- Cancellation: `sessionSIGINTCleanForObserver` is called by
  `session_live.go`, `session_duration_loop.go`, and
  `session_diagnostics_terminal.go`; the remaining SIGINT helpers are local
  compatibility seams exercised by cancellation/diagnostic tests.

## Active leases and exclusions

- C79 / `work-task-114` is the active exclusive writer for
  `scripts/wire-packages.txt`, `docs/architecture/architecture-size-baseline.json`,
  and its session-diagnostics fields. Neither shared registry is edited in this
  checkpoint. C79's latest board feedback remains separate evidence and is not
  absorbed into C104.
- The C104 plan explicitly excludes C79, C82, C84, C94, C96, C101, C103,
  `session_duration_loop.go`, `session_runtime_plan.go`, `service.go`, and both
  shared registries from this checkpoint. No peer-owned source is modified.
- Exclusion hashes at the census boundary:

  - `scripts/wire-packages.txt`: `3958443ed08ad6a550fd29ce8191c026d1a4017339e00c0b7406e0dcec7de05b`
  - `docs/architecture/architecture-size-baseline.json`: `6d61448b83eca374b324335f6a9e2fac768969d073b1091d17d48d45bf3badd1`
  - `agent-cli/internal/services/internal/agentruntime/session_duration_loop.go`: `2bf32471d10af9117600c5006d453fb12beb6a134e0461f0e6b895005270591f`
  - `agent-cli/internal/services/internal/agentruntime/session_runtime_plan.go`: `dd9c598e470816f4f5ec18c97059c55a23b04a0edcf39c94374433db1f70d33b`
  - `agent-cli/internal/services/internal/agentruntime/service.go`: `d3a1650dfb0a50b8cfcdf9376422d1e054f747a00e4b1951753cff6f9fca82b0`
  - `agent-cli/internal/services/internal/agentruntime/session_live.go`: `def8423286792fe4df6f47c44b6e4f6af09cb942f3c4c6cb7a82c39361ed2123`
  - `agent-cli/internal/services/internal/agentruntime/session_duration.go`: `4a9a70532bad77d453f67c434e8e91abc47b89b98a1a1dc79e2c77c5408aeb30`
  - `agent-cli/internal/services/internal/agentruntime/session_diagnostics_terminal.go`: `4608c5c35277c97375c89e989e8a9c88dc6376968239ee66d713a0ee3f65b369`

The candidate may add only the C104 public contract, private implementation,
dedicated Wire package, its focused tests/coverage manifests, three owned CLI
adapters/tests, and this task's evidence. Registry and baseline reconciliation
remain a later same-task step after explicit C79 guarded-merge lease release.
