# C43 implementation handoff

Date: 2026-09-10

This is the Luna executor handoff for the sole admitted `audio-runtime` project
and task `audio-runtime-c43-retire-cli-image-staging`. It is not independent
review, CI completion, merge approval, vertical acceptance, or project
completion.

## Admission and ancestry

- `rtk proxy python3 "$FACTORY_ROOT/factory/scripts/project-control.py" verify-work --type task --name audio-runtime-c43-retire-cli-image-staging` returned `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c43-retire-cli-image-staging"}`.
- The isolated worktree is `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c43-retire-cli-image-staging` on `codex/audio-runtime-c43-retire-cli-image-staging`; the admitted task packet's `prd.branchName` and worktree match these values.
- The current-main integration checkpoint is merge `3dfd88d3`; fetched `origin/main` is `d6efc88d10e046396e777762802f5f731c9c6d9d`. Required baseline/startup ancestry remains preserved. The running host checkout and predecessor worktrees were not merged, reset, or edited.
- The implementation checkpoint tested below is commit `97b1141b10b13c19ef73750694fa4acda4cc1bb2`.

## Review-254 repair map

- The prior rejection required a shipped executable, a deterministic local provider, real Chrome WebMCP dynamic refresh, an induced-timeout cleanup proof, exact source/build/fixture provenance, and a 142-to-71-line source/symbol inventory. `verify.py` now builds and runs the public `yui` executable and a loopback-only WebSocket provider, drives a real Chrome WebMCP page, records complete command/output/exit observations, and archives source/build hashes.
- The process probe exposed a live migration defect: `go-agent-runtime/services/session/internal/live` received semantic `tools_added`/`tools_removed`/navigation events but its refresh predicate only recognized legacy catalog/generation names. The predicate now refreshes for those semantic events; `start_support_test.go` covers the positive event types and a non-refresh invocation event in normal and race tests.
- The local provider completes the protocol with a normal WebSocket close frame after `session.closed`, and treats the induced timeout's client close (including code 1006) as the expected cleanup outcome. It also rejects any authorization header other than the synthetic hermetic test key.

## Exact-head focused evidence

Command:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c43-retire-cli-image-staging/verify.py --mode focused
```

Run: `runs/verify-20260910T200021Z-37030`; status `PASS`; 18 recorded steps;
tested source revision `97b1141b10b13c19ef73750694fa4acda4cc1bb2`.
The complete runner outcome, command arrays, stdout/stderr, exit codes, and
per-case observations are in that run's `outcome.json`,
`build-provenance.json`, and `process/*/observations.json` files.

Focused causal and accumulated checks all exited 0:

- public consumer build/test, positive exact-image proof, wrong-oracle negative control (expected exit 1 with `70` actual versus `71` expected), and post-cleanup negative control;
- live semantic capability refresh regression normal and `-race`;
- private image staging/Wire normal and race checks, CLI adapter normal and race checks, and all `go-agent-runtime/services/tools/...` regressions;
- in-process shipped CLI image regression;
- shipped process image workflow, induced timeout workflow, and credential-free strict tool replay;
- the process image workflow uses the literal 70-byte PNG, sends its exact staged absolute path to `read_image`, validates the typed `input_image` data URL, and observes the dynamic page tool `c43_refreshed_probe` in the second provider `session.update`.

Process provenance from `build-provenance.json`:

- `go env GOVERSION GOOS GOARCH`: `go1.26.7`, `darwin`, `arm64`;
- shipped CLI SHA-256: `6fa63456dac361c7bf9bdcf40030e54157e9f8e6cfda53ff4a1b83bf68526a6e`;
- local provider SHA-256: `7ac892d52f27bb1fcb426c6b83eb203f07bf5a1c3b08b1e5b1124e7961045c26`;
- provider source SHA-256: `f63381b1b55a10c095692938967a39a62bd77b6f50717c41278405aa97cd614a`;
- Chrome page source SHA-256: `703a8c098f75de45957f932a5cb979b5326befb24f8d21f66d6645db6918f698`;
- literal fixture: 70 bytes, SHA-256 `4ff6ab670a58c14270e034e2090d9a432caa263a14e0a25785386b0c12f880b5`;
- strict replay capture SHA-256: `b6541a87a39e065151f45553ff5b9e788e2d9d57576a15c285eeac071a1476ea`, config SHA-256: `84a450eec67059fa1d0a81ad80651ff4f04fdcee0f7585b514774c00f16e1d07`.

Image process observation: CLI exit `0`; provider status `PASS`; page events
`initial, refreshed`; initial catalog contains `c43_initial_probe`, refreshed
catalog contains both `c43_initial_probe` and `c43_refreshed_probe`; the
provider's validated image result and typed projection hash the fixture above;
staging leftovers are `[]`; CLI/provider/Chrome process-group survivors are
all `false`.

Induced-timeout observation: CLI exit `0`; provider status `PASS` with
`expected_timeout_client_close=true`; `--max-duration 2s`; staging leftovers
are `[]`; CLI/provider/Chrome process-group survivors are all `false`.

Credential-free replay observation: shipped CLI exit `0`, no timeout, no
process-group survivor, stdout contains `PROBE_TOOL_MARKER_9182` and
`strict replay continuation`, and the capture has no external provider or
credential dependency.

The required source delta is archived in the run's `source-delta.diff` and
`symbol-inventory.json`: baseline `926ded7bfa8f3c3e42115192d03aa1240c4806db`,
source `agent-cli/internal/services/internal/agentruntime/session_image_staging.go`,
142 baseline lines to 71 final adapter lines, source-delta SHA-256
`94652a9bec7dd6ee8241229e4d4aee24c57bcb9c44683d3c051981894403d6d4`.
Retired symbols are `sessionImageToolPathDescription`,
`sessionImageStageExtension`, and `advertiseSessionImagePaths`; retained
adapter symbols are `prepareSessionImageToolAccess` and
`sessionImageStagingConfigDir`.

## Handoff action and limits

Push commit `97b1141b10b13c19ef73750694fa4acda4cc1bb2` and its documentation
descendant to the existing PR `#434`, update the PR body with this evidence,
and submit the same task to the script-owned CI gate. Do not poll CI here and
do not claim green CI. If the script gate rejects a specific check, inspect
that exact log, repair this same task, and return `CONTINUE` while actionable
repairs remain.

This evidence is credential-free, loopback-only, and uses Chrome native
WebMCP. It does not prove physical microphone/speaker behavior, external
provider behavior, independent review, guarded merge, post-merge vertical
acceptance, or the immutable project-wide criteria.

## Current-main merge and architecture-fit checkpoint

The candidate fetched `origin/main` at
`7b6ce8cecee80d3f78b7aad02760067387853ade` and integrated it with merge
`5ca154f67ee95be1a61cddab99537912b424afb3`. The only merge conflict was the
shared `progress.txt`; both the C43 and incoming C36 ledger entries were kept.
The required baseline `926ded7b`, startup integration `8bdafc7f`, and current
main are ancestors of the candidate. The running host checkout, peer worktrees,
and predecessor checkpoints were not reset or edited.

The merged-head architecture gate first found the live package at 409/400
lines and 16/15 files because the C43 semantic refresh repair had added a
dedicated test file. The repair commit `169490ea69ff1886fc07ec333ac98dfa142aef05`
keeps the semantic `tools_added`, `tools_removed`, `page_navigated`, and
`frame_navigated` refresh predicate, compresses it to 399 lines, and removes
that newly introduced test file so the unchanged package budget is restored.
No architecture baseline or generated Wire file was changed. The resulting
architecture gate passes at 183 packages, 1,884 files, and 27,748 functions;
coverage registration passes at 173 packages.

Post-merge focused evidence currently available is limited to the tools
regression: normal and race `go test ./services/tools/...` both pass, including
the private imagestaging package. The first merged-head CLI image test attempt
was the exact command
`rtk proxy sh -c 'cd agent-cli && go test -tags=nomicrophone ./internal/services/internal/agentruntime -run "Image|ReadImage" -count=1 -timeout=120s'`;
it failed during linking with `no space left on device`, before any test ran.
The filesystem then reported 270 MiB free, below the recorded 2 GiB build
reserve. This is an operator storage prerequisite, not a code result; no cache,
evidence, peer artifact, or unrelated worktree was deleted.

The earlier exact-process run `verify-20260910T200021Z-37030` remains historical
evidence for source `97b1141b`; it is not relabeled as proof for the merged
candidate. The next action is to restore measured headroom without deleting
retained evidence, rerun the focused verifier and merged-head CLI normal/race
checks, record new source/build/fixture hashes, then push/update PR #434 and
return `ACCEPTED` to script CI only if those current-head checks complete.

## Fresh merged-head exact-process checkpoint — 2026-09-10T21:11Z

The documented storage prerequisite cleared to 7.3 GiB free. The exact bounded
command
`rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c43-retire-cli-image-staging/verify.py --mode focused`
passed as run `verify-20260910T211145Z-67634` at source
`169490ea69ff1886fc07ec333ac98dfa142aef05`. The complete outcome, commands,
stdout/stderr, exit codes, observations, build provenance, source delta and
symbol inventory are retained under that run directory. All 18 steps passed;
the deliberate wrong-oracle step exited 1 with the expected `70 actual versus
71 expected` diagnostic, while the other 17 steps exited 0 without timeout.

The fresh causal/regression controls include public tools-Wire positive and
cleanup checks, the wrong-oracle negative, semantic capability refresh in
normal and race modes, private imagestaging/Wire normal and race tests, CLI
adapter normal and race tests, all tools-service regressions, the in-process
CLI image workflow, and the credential-free strict replay regression.
`make architecture-size-check` passes at 183 packages, 1,884 files and 27,748
functions; `make coverage-registration` passes at 173 workspace packages;
`make wire-check` regenerated the checked-in Wire outputs with no tracked
changes; `make lint` exits 0 with pinned golangci-lint 2.9.0; `make staticcheck`
exits 0 with pinned staticcheck 2026.1; and `git diff --check` passes.

The fresh process observations are tied to the rebuilt artifacts. The image
workflow exits 0; the loopback provider reports `PASS`; real Chrome native
WebMCP reports page events `initial, refreshed`; the initial catalog contains
`c43_initial_probe`, the refreshed catalog adds `c43_refreshed_probe`, and the
provider validates the exact 70-byte PNG and typed `input_image` projection.
The staging directory has no leftovers and CLI/provider/Chrome process-group
survivors are all false. The induced `--max-duration 2s` workflow exits 0 with
provider `PASS`, `expected_timeout_client_close=true`, no staging leftovers and
no process survivors. Credential-free replay exits 0 with
`PROBE_TOOL_MARKER_9182`, `strict replay continuation`, and no survivor.

Fresh provenance in `build-provenance.json` records Go `go1.26.7`, Darwin
arm64, loopback-only networking, CLI SHA-256
`05e617f416b98e577437bf31eb1999eac147600124a8dea2115499bc16da0ad1`, local
provider SHA-256
`7ac892d52f27bb1fcb426c6b83eb203f07bf5a1c3b08b1e5b1124e7961045c26`, provider
source SHA-256
`f63381b1b55a10c095692938967a39a62bd77b6f50717c41278405aa97cd614a`, page
source SHA-256
`703a8c098f75de45957f932a5cb979b5326befb24f8d21f66d6645db6918f698`, and
literal fixture SHA-256
`4ff6ab670a58c14270e034e2090d9a432caa263a14e0a25785386b0c12f880b5` for 70
bytes. The strict replay capture SHA-256 is
`b6541a87a39e065151f45553ff5b9e788e2d9d57576a15c285eeac071a1476ea` and its
config SHA-256 is
`84a450eec67059fa1d0a81ad80651ff4f04fdcee0f7585b514774c00f16e1d07`.

The source inventory in the same run records the required 142 baseline lines
to 71 final adapter lines and retires
`sessionImageToolPathDescription`, `sessionImageStageExtension`, and
`advertiseSessionImagePaths`, while retaining
`prepareSessionImageToolAccess` and `sessionImageStagingConfigDir`. The source
delta SHA-256 is
`94652a9bec7dd6ee8241229e4d4aee24c57bcb9c44683d3c051981894403d6d4`.

The final documentation checkpoint is a docs-only descendant of the tested
source `169490ea69ff1886fc07ec333ac98dfa142aef05`. Its only changed paths
relative to the tested source are this task's README,
implementation handoff, PR body and root `progress.txt`; no executable build,
fixture, provider or browser-page input changed. The run artifact is therefore
retained as an evidence-only descendant with explicit source/input equivalence,
not relabeled as a build from a different implementation.

This is current implementation evidence only: no terminal CI result,
independent review, guarded merge, post-merge vertical acceptance, physical or
acoustic proof, or project acceptance is claimed. The next action is to commit
this documentation/evidence checkpoint, push the same branch, update PR #434
with the exact current head and review-254 repair map, and return `ACCEPTED` to
the script-owned CI gate without polling it. Retain this task through
`CONTINUE` for any exact CI rejection.

## Current-head merge and storage prerequisite checkpoint — 2026-09-10

The canonical task/review inbox was re-read and admission reverified. The
isolated branch still exactly matches `prd.json.branchName`. Fetched
`origin/main=5f14c45313cfdc71e000fda209e3408fcf863faf` was merged as
`ac77c076`; the only conflict was the shared `progress.txt`, with both C43 and
incoming C44 ledger entries retained. Baseline `926ded7b`, startup integration
`8bdafc7f`, and current main are ancestors of the candidate. The merge and
follow-up storage checkpoint are pushed at `7a07f811` on PR #434, which is open
and currently reported mergeable.

Post-merge bounded checks pass: tools and CLI image normal/race tests, live
package normal/race tests, `make fmt wire-check vet size-check`, pinned
golangci-lint 2.9.0 with 0 issues, pinned staticcheck 2026.1,
`make architecture-size-check` at 184 packages/1,888 files/27,779 functions,
and `make coverage-registration` at 174 packages. Wire regeneration produced
no tracked changes and `git diff --check` is clean.

The required fresh executable verifier was not run because free space fell from
2.3 GiB before the bounded quality checks to 1,794,788 KiB afterward, below the
required 2 GiB reserve and C43's 256 MiB attributable-growth cap. No cache,
evidence, peer artifact or unrelated worktree was deleted. The historical
`169490ea` process artifact is not relabeled as current-head proof because the
merged main changed executable inputs. After the operator restores stable
headroom, run the exact bounded command
`rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c43-retire-cli-image-staging/verify.py --mode focused`,
record fresh provenance, update this handoff and PR #434, then submit the same
task to script CI without polling. This remains an executor `CONTINUE` checkpoint;
CI, independent review, guarded merge, vertical acceptance and project
acceptance are not claimed.
