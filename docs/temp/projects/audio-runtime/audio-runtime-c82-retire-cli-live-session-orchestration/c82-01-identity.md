# C82-01 identity, ancestry and ownership checkpoint

Task: `audio-runtime-c82-retire-cli-live-session-orchestration`
Project: `audio-runtime`
Contract revision: `audio-runtime-v1`
Factory session: `~default`

## Admission and source identity

- `project-control.py verify-work --type task --name audio-runtime-c82-retire-cli-live-session-orchestration` returned `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c82-retire-cli-live-session-orchestration"}`.
- `prd.json.branchName` is `codex/audio-runtime-c82-retire-cli-live-session-orchestration`, matching the isolated worktree branch.
- The admitted project is the sole `audio-runtime` project under the immutable `audio-runtime-v1` manifest; no second project or acceptance waiver is used.
- The worktree was clean before this evidence checkpoint at `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`.
- `git fetch origin main` completed; current `origin/main` is `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`.
- Required ancestry checks pass for startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21`, accepted/planning main `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`, and fetched `origin/main`.

## Baseline and owned surface

At accepted main `84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`:

- `agent-cli/internal/services/internal/agentruntime/session_live.go`: 1,061 physical lines.
- `agent-cli/internal/services/internal/agentruntime/session_live_setup.go`: 72 physical lines.
- Exact production baseline: 1,133 lines.

C82 owns those two production files, their named direct tests, the new
`go-agent-runtime/services/sessionlive/**` contract/private/Wire packages, the
matching coverage manifest, and this evidence directory. C82 does not own the
peer C64/C73-C81 source paths, `session_runtime_plan.go`,
`session_room_coordinator.go`, `session_options.go`, `session_tools.go`,
`session_diagnostics*.go`, `rtc_device_runtime.go`, the existing
`go-agent-runtime/services/session/internal/live/lifecycle.go`,
`scripts/wire-packages.txt`, or
`docs/architecture/architecture-size-baseline.json`.

## Review and predecessor state

- The factory board has no prior C82 review identity or review findings; only
  the C82 planning/setup workers completed before this executor.
- No predecessor checkpoint or unrelated worktree was modified. The running
  host checkout was not used for integration, reset, or merge.
- Script CI, independent review, guarded merge, and the post-merge immutable
  vertical probe remain open gates; this checkpoint makes no acceptance claim.

## Next action

Create the host-neutral `sessionlive` contract/private implementation and
dedicated Wire graph, migrate the live-loop lifecycle behind a thin CLI
adapter, and add focused normal/race/consumer controls without touching peer
paths or the leased shared registries.
