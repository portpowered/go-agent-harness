# C115 current-head bounded revalidation checkpoint

## Identity and preservation

- `project-control.py verify-work --type task --name audio-runtime-c115-centralize-filesystem-audio-codec --root "$FACTORY_ROOT"` returned `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c115-centralize-filesystem-audio-codec"}`.
- The sole admitted project is `audio-runtime/audio-runtime-v1`; Factory session is `~default` at `http://127.0.0.1:7439`. The isolated branch `codex/audio-runtime-c115-centralize-filesystem-audio-codec` matches `prd.json.branchName`, and the worktree was clean before this checkpoint.
- Fresh `origin/main` is `ea53be13ce5e4ef14fd8c89c695c21744a1f7686`; startup `8bdafc7f947a3a2c9856220abdc539437035bd21`, accepted/planning main `3963bc3566da24f8214634c17a9d0f79a6724171`, and fresh main are all ancestors of `f68821fc819102bc52762a06bf9a09bfacae5324`. No host checkout, peer worktree, predecessor checkpoint, second project, waiver, reset, or C79-owned shared path was changed.

## Fresh bounded evidence

- Audiocodec focused normal: `26` tests across `3` packages.
- Audiocodec focused race: `105` tests across `3` packages at `-count=3`.
- Filesystem focused normal: `58` tests in `1` package.
- Filesystem focused race: `57` tests in `1` package at `-count=3`.
- Separate `GOWORK=off` external consumer: pass.
- `make test-regressions`: pass for the agent-cli and go-llm-gateway replay fixtures.
- `verify.py --mode public-and-accumulated-regressions`: pass, candidate revision `f68821fc819102bc52762a06bf9a09bfacae5324`; its source-bound verification summary was refreshed.
- `make wire-check`: pass; generated files remain unchanged.
- `make architecture-size-check`: fails only the two existing non-C115 baseline drifts: `agent-cli/internal/transport/cli` (`148 > 147`) and `internal/transport/cli/room.go` (`535 > 527`).
- Accepted-main `tool_filesystem.go` remains `261` lines / SHA-256 `6f67450d01cda17896afcc7a302dc024401b4fb56c163cb5eb625546e2197679`; current adapter is `228` lines / SHA-256 `a086ff1a7366cd59891bdfe6b5f2e9237d9079389530affa5ef46dd28bd20e2e`, a strict `33`-line reduction.

## Lease-gated next action

C79 `work-task-25` remains `ci-pending` on PR #470 at `b5e11e47d`; there is no reviewed guarded merge or explicit release of the shared architecture-release lane. C115 PR #501 remains open at pushed head `f68821fc`; its current CI run is in progress and is not claimed or polled. Retain this same task. After C79's reviewed guarded release, fetch and integrate the accepted current main without resetting the running host checkout, apply only demonstrated C115 shared changes if still needed, rerun the final scoped and accumulated gates, push the changed same PR head, and submit it to Script CI without polling. Fresh independent review, guarded merge, and the immutable engineering vertical probe remain external requirements.
