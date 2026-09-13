# C115 checkpoint 5b23b484

## Identity and ancestry

- Project admission: `audio-runtime/audio-runtime-v1`; `project-control.py verify-work --type task --name audio-runtime-c115-centralize-filesystem-audio-codec` returned `admitted`.
- Factory session/server: `~default` / `http://127.0.0.1:7439`.
- Worktree and branch: `audio-runtime-c115-centralize-filesystem-audio-codec` /
  `codex/audio-runtime-c115-centralize-filesystem-audio-codec`.
- Required startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21`, accepted/planning main `3963bc3566da24f8214634c17a9d0f79a6724171`, and fetched `origin/main=3963bc3566da24f8214634c17a9d0f79a6724171` are ancestors of `5b23b484`.
- The worktree was clean before the repair and is clean after the pushed checkpoint. No host checkout, peer worktree, predecessor checkpoint, shared registry, or shared baseline was reset or edited.

## Baseline and repair

- Accepted-main `go-agent-runtime/services/tools/internal/filesystem/tool_filesystem.go` remains the 261-line file with SHA-256 `6f67450d01cda17896afcc7a302dc024401b4fb56c163cb5eb625546e2197679`.
- `audioToPCM16k` remains the sole production conversion caller edge from `tool_file_tools.go`; that caller is unchanged and read-only under the admitted PRD.
- Commit `5b23b484` splits format detection, decoder wait/limit classification, conversion request validation, temporary-file decoding, and PCM error mapping into bounded private helpers. This repairs all five owned architecture complexity findings without changing the public contract or the filesystem adapter's current Wire call.

## Bounded evidence

- `go test ./go-agent-runtime/services/audiocodec/... -count=1 -timeout=240s`: PASS, 34 tests in 3 packages.
- Existing focused filesystem regression: PASS, 58 tests in 1 package.
- `git diff --check`: PASS.
- `make architecture-size-check` after the repair: FAIL, exactly 4 findings:
  - `forbidden-import`: filesystem adapter imports `audiocodec/wire`.
  - `peer-private-import`: filesystem adapter imports peer service `audiocodec/wire`.
  - `wire-import`: only composition packages may import a service Wire package.
  - `generated-file-spoof`: `audiocodec/wire/wire_gen.go` is not registered.
- `make wire-check`: FAIL only because `go-agent-runtime/services/audiocodec/wire/wire_gen.go` is not in `scripts/wire-packages.txt`.

## Exact remaining decision

C79 `work-task-114` is still `ci-pending` and retains exclusive ownership of
`scripts/wire-packages.txt` and `docs/architecture/architecture-size-baseline.json`.
The PRD also keeps `tool_file_tools.go` read-only, while the current adapter's
direct `audiocodec/wire.NewService()` call is the four-finding composition
violation. The next executor action requires an explicit ownership/lease
transfer (or authorized replan) for the legal host composition seam, followed
by C79's reviewed guarded release before the audiocodec Wire registration and
any demonstrated downward/stale baseline edit. No CI handoff is made from
this structurally blocked head.
