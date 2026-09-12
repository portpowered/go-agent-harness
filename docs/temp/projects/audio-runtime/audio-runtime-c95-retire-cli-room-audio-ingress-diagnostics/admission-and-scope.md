# C95 admission and scope evidence

The admitted Factory project is `audio-runtime`, contract `audio-runtime-v1`,
session `~default`. No second Factory project or acceptance waiver was used.

Admission command, run from `FACTORY_ROOT`:

```text
rtk python3 "$FACTORY_ROOT/factory/scripts/project-control.py" verify-work --type task --name audio-runtime-c95-retire-cli-room-audio-ingress-diagnostics
{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c95-retire-cli-room-audio-ingress-diagnostics"}
```

The admitted branch is
`codex/audio-runtime-c95-retire-cli-room-audio-ingress-diagnostics`, matching
`prd.json.branchName` and the isolated worktree. The starting planning-main
revision is `3d3e72786ac6fc1fd47c7e029589e5117674b035`; required startup
integration ancestry `8bdafc7f947a3a2c9856220abdc539437035bd21` and manifest
baseline ancestry `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` are preserved.
`origin/main` was fetched before implementation and the running host checkout
was not merged, reset, or otherwise changed.

The exact admitted predecessor is
`agent-cli/internal/services/internal/agentruntime/session_room_audio_diagnostics.go`:

```text
baseline physical lines: 472
baseline SHA-256: 02a7b14d7104329fc71f8f8f97dc59fa26830d2f48dc29283a1a933455dff9c9
final adapter physical lines: 170
```

The adapter now forwards the public contract through the dedicated runtime
Wire graph. The private FIFO, classification, attribution, rejection,
bounded-observation, cumulative-total, pending-loss, and finish policy lives
under `go-agent-runtime/services/roomaudiodiagnostics/internal/service`.

The C79 task (`work-task-114`, PR #470) remains `ci-pending` and owns the
shared `scripts/wire-packages.txt` registry and
`docs/architecture/architecture-size-baseline.json`. C95 deliberately leaves
both files unchanged. The current handoff prerequisite is an explicit C79
lease release plus primary assignment for registering this generated Wire
package and removing the two stale predecessor-baseline entries.
