# C109 fresh focused revalidation

Timestamp: `2026-09-12T22:35:27Z`

This is a fresh software-only revalidation of the C109 evidence repair. It
changes only the C109-owned directory. C61, C83, C79-owned shared files,
candidate source, and the running host checkout were not changed.

Admission and provenance:

- `project-control.py verify-work --type task --name audio-runtime-c109-characterize-browser-retirement-integration` returned `admitted` for project `audio-runtime`.
- branch `codex/audio-runtime-c109-characterize-browser-retirement-integration` matches `prd.branchName`.
- accepted main `d4766c3dbbf2c198142047ead4449d58dd47d485` was fetched and remained the review main.
- C61 `8e8177c031a7b3b9322d712af19970e13fa7a1bc` and C83 `22cc6769aaf06d1e2c1275b064cc7ec29de3e371` candidate refs remained unchanged.
- the expected clean C61/C83 predecessor worktrees were present at their pinned heads before and after both analyzer orders.

Merge-order evidence:

- required analyzer: `main -> C61 -> C83`, passed; candidate refs and preserved worktrees unchanged.
- reverse control analyzer: `main -> C83 -> C61`, passed; candidate refs and preserved worktrees unchanged.
- pair verifier passed source-driven provenance/ledger, merge-order, behavior/attribution, and handoff-sequence checks.
- both orders converged to synthetic tree `4d8e93ae0e74db403fdb02c70db9cf849c590156`.
- exact manifest gate set is nine gates, all `OPEN`; C61/C83/project delivery claims remain false.
- `verify.py --mode all` passed determinism, all 13 negative mutations, and both public reports; `runs/final/verification.json` records `status: passed`.

## `browser-audio-tool`

Command:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/run_public_checks.py --case browser-audio-tool --output /tmp/c109-public-browser-v4.json
```

Result: `passed`; elapsed `250.291s`; aggregate bound `900s`, child bound
`480s`; final public report SHA-256
`2fc707406e5d4a3f3fb907b27476eab056d62eb692daa210462b9f507fef3617`.

All twelve checks passed with exit code 0: browserrunner/browserscenario
normal and race; C61 and C83 external consumers normal/race with `GOWORK=off`;
the accepted-main delta barrier regression; CLI cancellation normal/race;
nomicrophone cross-module coverpkg cancellation; accumulated normal/coverage/
race session CI regressions; and the shipped credential-free browser/audio/tool
workflow. Test discovery was positive for every Go test command. The detailed
shipped report retained binary/audio/PCM/terminal hashes and classified the
effects as `SOFTWARE_LOCAL_PROCESS_ONLY`.

## `malformed-or-canceled`

Command:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/run_public_checks.py --case malformed-or-canceled --output /tmp/c109-public-malformed-v3.json
```

Result: `passed`; final public report SHA-256
`29abad30fed9a5e9b9f24bcba9e6bd99e828196e417f4eb83014b6165d88ae01`.

All three checks passed with exit code 0: malformed credential overflow,
timeout and cleanup; canceled browser scenario normal; and canceled browser
scenario race. A clean C61-only worktree supplied via `--tree` was separately
rejected with `synthetic HEAD is not the second merge commit`, proving the
public runner does not accept an arbitrary tree.

The final evidence remains handoff-only: no CI-green, review, merge, vertical
acceptance, hardware, acoustic, or project-acceptance claim is made. The
historical C61 rewritten-test finding remains
`OPEN_RECONCILE_TEST_REWRITTEN`, and the 6,400-sample provider-audio loss
remains assigned to `C79/provider-audio`.
