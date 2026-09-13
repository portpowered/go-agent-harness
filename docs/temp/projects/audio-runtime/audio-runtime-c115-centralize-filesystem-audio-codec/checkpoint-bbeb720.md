# C115 post-C79 integration and architecture-fit checkpoint

## Identity and released dependency

- `project-control.py verify-work --type task --name audio-runtime-c115-centralize-filesystem-audio-codec --root "$FACTORY_ROOT"` remains admitted to the sole `audio-runtime/audio-runtime-v1` project. The isolated branch matches `prd.json.branchName`.
- C79's reviewed guarded merge is PR #470 merge commit `1a8467246c6607a06ffc7289075da2595724ce8b`; its task is terminal complete and its shared Wire/architecture lease is released.
- This branch integrated freshly fetched `origin/main=1a8467246c6607a06ffc7289075da2595724ce8b` with merge commit `a654ced7abdf85bb55b70395c35f9199a00f6216`. Startup `8bdafc7f947a3a2c9856220abdc539437035bd21`, accepted/planning main `3963bc3566da24f8214634c17a9d0f79a6724171`, and fresh `origin/main` are ancestors.
- Source repair commit `bbeb72042b0247fae370b3d7e7cc6c8c25ea6578` keeps the filesystem adapter at 228 lines with SHA-256 `a086ff1a7366cd59891bdfe6b5f2e9237d9079389530affa5ef46dd28bd20e2e`, consolidates the new test helper into an existing test file, keeps `room.go` at 525 lines, lowers only its observed baseline from 527 to 525, and registers `go-agent-runtime/services/audiocodec/wire` in the released ordered Wire registry.

## Focused and accumulated gates

- Audiocodec focused normal: 26 tests across 3 packages; focused race: 105 tests across 3 packages at `-count=3`.
- Filesystem focused normal: 58 tests in 1 package; focused race: 57 tests in 1 package at `-count=3`.
- CLI/tools composition focus: 480 normal tests across 19 packages; the standalone `GOWORK=off` consumer passes; `make test-regressions` passes the agent-cli and five go-llm-gateway replay-fixture groups.
- `make fmt`, `make build`, `make vet`, pinned golangci-lint 2.9.0, pinned staticcheck 2026.1, `make coverage-registration` (182 packages across 6 modules), `make wire-check`, `make architecture-size-check` (192 packages, 1,916 files, 28,298 functions), and `git diff --check` pass.
- The bounded source-pinned run `runs/20260913T081732Z-9783/report.json` and `verify.py --mode public-and-accumulated-regressions` pass all five cases. The exact `nomicrophone -trimpath` `yui` artifact is 51,139,010 bytes with SHA-256 `9507b8b4177e0037245263ad030330a636aed1afd671c071b898b003929ba879`; evidence is bound to `bbeb720` and records clean process-group shutdown. Physical device consumption and acoustic proof remain unknown/out of scope.

## Preserved separate regression evidence

- An additional local race characterization of `TestSessionCommandReplaysTest7LongOpenAIAudioTo16kLoopback` failed once at 766,080/810,400 samples in a larger composition race run and failed again in its exact standalone race invocation at 786,240/810,400; the normal exact test passes. The test and provider/session audio paths are outside the C115 lease, no C115 source change was made for it, and the required C115 accumulated `make test-regressions` target remains green. This is retained as peer/out-of-scope evidence, not claimed fixed or waived.

This is an executor checkpoint only: no current-head script-CI result, independent review, guarded merge of PR #501, immutable vertical probe, or project acceptance is claimed. Next action: commit the evidence refresh, push the same PR #501 branch, update it with exact head/base and gates, and return `ACCEPTED` to the script CI gate without polling; retain this task for any exact actionable rejection.
