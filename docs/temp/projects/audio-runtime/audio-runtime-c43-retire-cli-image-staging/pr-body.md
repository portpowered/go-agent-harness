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
- Tested source commit:
  `97b1141b10b13c19ef73750694fa4acda4cc1bb2`.
- Current-main integration merge: `3dfd88d3`; fetched `origin/main`:
  `d6efc88d10e046396e777762802f5f731c9c6d9d`; required ancestry is preserved.
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
  normal/race regression is
  `TestCapabilityEventRequiresRefreshForSemanticCatalogMutations`.
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

## Handoff

This is an executor handoff only. Push/update existing PR `#434` at the same
task, then submit it to the script-owned CI gate. This PR does not claim green
CI, independent review, merge, post-merge vertical acceptance, physical or
acoustic proof, or project-wide acceptance. On an exact CI rejection, inspect
the failed check/log, repair this same task, and resubmit.
