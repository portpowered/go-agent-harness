# C109 focused revalidation

Timestamp: `2026-09-13T00:18:35Z`

This is a fresh software-only revalidation of the C109 evidence repair. It
changes only the admitted C109-owned directory. C61, C83, C79-owned shared
files, candidate source, predecessor worktrees, and the running host checkout
were not changed.

Admission and provenance:

- `project-control.py verify-work --type task --name audio-runtime-c109-characterize-browser-retirement-integration` returned `admitted` for project `audio-runtime`.
- branch `codex/audio-runtime-c109-characterize-browser-retirement-integration` matches `prd.branchName`.
- admitted accepted baseline is `d4766c3dbbf2c198142047ead4449d58dd47d485`; fetched origin/main `3963bc3566da24f8214634c17a9d0f79a6724171` was newer and integrated in this isolated C109 branch.
- C61 `8e8177c031a7b3b9322d712af19970e13fa7a1bc` and C83 `22cc6769aaf06d1e2c1275b064cc7ec29de3e371` candidate refs remained unchanged.
- the expected clean C61/C83 predecessor worktrees were present at their pinned heads before and after both analyzer orders.
- final required/control evidence binds its analyzer/verifier source to committed source head `35a5c90a06b24e18fd3f429b6e18be5f752817aa`.

Merge-order and causal evidence:

- required analyzer: `main -> C61 -> C83`, passed; candidate refs and preserved worktrees unchanged.
- reverse control analyzer: `main -> C83 -> C61`, passed; candidate refs and preserved worktrees unchanged.
- `verify.py --mode merge-orders` passed source-driven provenance/ledger and both merge rehearsals.
- both orders converged to synthetic tree `8372c75f4b2e8181148ac000d6dcd5db26414630`.
- the final ledger covers 70 changed paths, 3,408 source-derived hunk caller edges, and an API scan of 15 C83 Go paths with 372 source-derived edges; `c83_requires_c61_api` is derived `true`.
- `ci-attribution.json` retains historical C83 run `34713381619`, exact C83 head `22cc6769aaf06d1e2c1275b064cc7ec29de3e371`, seven passing lanes, and the static/integration failures with exact signatures and owners. The 6,400-sample loss remains assigned to `C79/provider-audio`; C109 does not repair or relabel it.
- `verify.py --mode all --write runs/final/verification.json` passed provenance, merge orders, behavior/attribution, handoff sequence, determinism, all 13 report negatives, both public reports, and five public negatives. The recorded result is `status: passed`.
- the manifest contains nine broad gates, all `OPEN`; C61, C83, project acceptance, review, merge, and vertical-probe claims remain false.

## `browser-audio-tool`

Command:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/run_public_checks.py --case browser-audio-tool --output /tmp/c109-public-browser-current.json
```

Result: `passed`; elapsed `246.323s`; aggregate bound `900s`, child bound
`480s`; final public report SHA-256
`89fd4b5e36f84adc39891baad1aa5d40586462fbb8ce8b5cee4ad50e8837701c`.

All twelve checks passed with exit code 0: browserrunner/browserscenario
normal and race; C61 and C83 external consumers normal/race with `GOWORK=off`;
the accepted-main delta barrier regression; CLI cancellation normal/race;
nomicrophone cross-module coverpkg cancellation; accumulated normal/coverage/
race session CI regressions; and the shipped credential-free browser/audio/tool
workflow. Every child report was uncapped and its raw/retained output hashes
matched; process groups and temporary worktree cleanup were clean. The
detailed shipped report retained binary/audio/PCM/terminal hashes and
classified effects as `SOFTWARE_LOCAL_PROCESS_ONLY`.

## `malformed-or-canceled`

Command:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/run_public_checks.py --case malformed-or-canceled --output /tmp/c109-public-malformed-current.json
```

Result: `passed`; elapsed `30.399s`; final public report SHA-256
`c96b78ef95dc51e2522db0279db8bdcdddb5f56c3cdb8793410545bce9ae79c6`.

All three checks passed with exit code 0: malformed credential overflow,
timeout and cleanup; canceled browser scenario normal; and canceled browser
scenario race. The negative control was software-only and had no external
browser or microphone effect.

## Caller-owned `--tree` control

Command:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/run_public_checks.py --case malformed-or-canceled --tree /tmp/c109-caller-tree-current --output /tmp/c109-public-malformed-caller-tree.json
```

Result: `passed`; elapsed `33.181s`; synthetic tree remained
`4d8e93ae0e74db403fdb02c70db9cf849c590156`. The caller-owned tree was
preserved, while the runner's auxiliary temporary synthetic tree was removed
successfully (`auxiliary_cleanup.status: passed`). A clean C61-only tree was
also rejected by the synthetic ancestry checks; arbitrary trees cannot be
used as public evidence.

The final evidence remains handoff-only: no CI-green, review, merge, vertical
acceptance, hardware, acoustic, or project-acceptance claim is made. C61 and
C83 remain unmerged and unaccepted pending their own task/review/CI gates.
