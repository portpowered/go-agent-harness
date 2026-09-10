## Summary

Retire CLI-owned image staging while preserving the public `read_image` tool
contract. The public tools Wire/executor now owns staging, permission policy,
path advertisement, refresh decoration, typed image projection, and cleanup;
the CLI retains host configuration resolution and composition.

## Admitted candidate

- Sole admitted project/task: `audio-runtime` /
  `audio-runtime-c43-retire-cli-image-staging`.
- Branch and isolated worktree match the admitted `prd.branchName`:
  `codex/audio-runtime-c43-retire-cli-image-staging`.
- Historical tested source commit:
  `97b1141b10b13c19ef73750694fa4acda4cc1bb2`.
- Current-main integration is preserved through merge
  `5ca154f67ee95be1a61cddab99537912b424afb3`; fetched `origin/main` is
  `7b6ce8cecee80d3f78b7aad02760067387853ade`; the current structural repair
  descendant is `169490ea69ff1886fc07ec333ac98dfa142aef05`.
- No architecture baseline, generated Wire file, peer task path, host
  checkout, acceptance waiver, or unrelated change was modified.

## Implementation and review repairs

- The prior Review-254 gap is closed with a bounded exact-head process
  harness: built `yui`, a deterministic loopback WebSocket provider, real
  Chrome native WebMCP, dynamic refresh, induced timeout, child/staging
  cleanup, and complete source/build/fixture provenance.
- The process probe found and repaired the live semantic event gap: the
  runtime now refreshes on `tools_added`, `tools_removed`, and page/frame
  navigation events in addition to legacy catalog/generation events. The
  predicate remains in the merged candidate; its dedicated test file was
  removed to restore the unchanged live package file ceiling, so the prior
  process evidence is explicitly historical until rerun on the merged head.
- The local provider uses a normal close frame after `session.closed`, accepts
  the induced timeout's expected abnormal client close, and rejects ambient
  authorization by requiring only the synthetic hermetic key.
- The static-rejection repairs from the predecessor checkpoint remain in
  place: cleanup failures are logged/joined and the existing 142-to-71-line
  CLI adapter retirement is preserved.

## Validation

`rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c43-retire-cli-image-staging/verify.py --mode focused`
passed as run `verify-20260910T200021Z-37030` with 18 steps at the tested
source revision. Consumer positive, wrong-oracle negative, cleanup-negative,
live refresh normal/race, private staging normal/race, CLI adapter normal/race,
all tools regressions, in-process CLI image, shipped process image, induced
timeout, and credential-free strict replay all passed their expected outcomes.

The built-process evidence proves:

- image case: CLI exit `0`, provider `PASS`, page events `initial, refreshed`,
  second provider `session.update` contains `c43_refreshed_probe`, exact
  70-byte PNG SHA-256
  `4ff6ab670a58c14270e034e2090d9a432caa263a14e0a25785386b0c12f880b5`, no
  staging leftovers, and no CLI/provider/Chrome process survivors;
- induced-timeout case: CLI exit `0`, provider `PASS` with
  `expected_timeout_client_close=true`, `--max-duration 2s`, no staging
  leftovers, and no process survivors;
- strict replay: shipped CLI exit `0`, no timeout or survivor, and stdout
  contains `PROBE_TOOL_MARKER_9182` and `strict replay continuation`.

Provenance is archived in `implementation-handoff.md` and the run records:
CLI SHA-256 `6fa63456dac361c7bf9bdcf40030e54157e9f8e6cfda53ff4a1b83bf68526a6`,
provider SHA-256
`7ac892d52f27bb1fcb426c6b83eb203f07bf5a1c3b08b1e5b1124e7961045c26`, and
Chrome fixture SHA-256
`703a8c098f75de45957f932a5cb979b5326befb24f8d21f66d6645db6918f698`.
The source inventory records 142 baseline lines, 71 final adapter lines, and
retired symbols `sessionImageToolPathDescription`,
`sessionImageStageExtension`, and `advertiseSessionImagePaths`.

## Current-head checkpoint

The merged candidate passes `make architecture-size-check` at 183 packages,
1,884 files, 27,748 functions and `make coverage-registration` at 173
packages. Wire regeneration produced no tracked changes and `git diff --check`
is clean. Pinned `make lint` and `make staticcheck` both exit 0 using
golangci-lint 2.9.0 and staticcheck 2026.1. Fresh merged-head verifier run
`verify-20260910T211145Z-67634` at source
`169490ea69ff1886fc07ec333ac98dfa142aef05` passed all 18 bounded steps,
including normal/race tools and adapter regressions, the in-process image
workflow, built `yui` image process workflow, induced `--max-duration 2s`
cleanup, and credential-free replay. The deliberate wrong-oracle control
exited 1 with the expected 70-versus-71 mismatch; all other steps exited 0.

The built image workflow exited 0 with loopback provider `PASS`, real Chrome
WebMCP page events `initial, refreshed`, exact 70-byte PNG/readback and typed
projection, refreshed `c43_refreshed_probe`, zero staging leftovers, and no
CLI/provider/Chrome process survivors. The induced timeout also exited 0 with
`expected_timeout_client_close=true`, zero staging leftovers and no survivors.
Fresh build provenance records CLI SHA-256
`05e617f416b98e577437bf31eb1999eac147600124a8dea2115499bc16da0ad1`, provider
SHA-256 `7ac892d52f27bb1fcb426c6b83eb203f07bf5a1c3b08b1e5b1124e7961045c26`,
page SHA-256 `703a8c098f75de45957f932a5cb979b5326befb24f8d21f66d6645db6918f698`,
and fixture SHA-256
`4ff6ab670a58c14270e034e2090d9a432caa263a14e0a25785386b0c12f880b5`.

The earlier 270 MiB/no-space observation remains historical and is not used as
a current-head result. The exact source inventory remains 142 baseline lines
to 71 adapter lines, retiring `sessionImageToolPathDescription`,
`sessionImageStageExtension`, and `advertiseSessionImagePaths` while retaining
`prepareSessionImageToolAccess` and `sessionImageStagingConfigDir`.

The PR documentation is a docs-only descendant of the tested executable source
`169490ea69ff1886fc07ec333ac98dfa142aef05`: only the C43 README, handoff, PR
body and root progress ledger changed, so all executable build inputs are
identical and the recorded binary/protocol evidence remains valid without
relabeling it.

## Handoff

This is an executor handoff only. Commit/push this current evidence checkpoint,
update existing PR `#434` at the same task, and submit it to the script-owned CI
gate. This PR does not claim
green CI, independent review, merge, post-merge vertical acceptance, physical
or acoustic proof, or project-wide acceptance. On an exact CI rejection, inspect
the failed check/log, repair this same task, and resubmit.
