# C109 focused revalidation

Timestamp: `2026-09-13T01:43:02Z`

This software-only revalidation repairs the public evidence runner's fail-open
test discovery and caller-owned tree handling. It changes only the admitted
C109-owned directory. C61, C83, C79-owned shared files, candidate source,
predecessor worktrees, and the running host checkout were not changed.

Admission and provenance:

- `project-control.py verify-work --type task --name audio-runtime-c109-characterize-browser-retirement-integration` returned `admitted` for project `audio-runtime`.
- branch `codex/audio-runtime-c109-characterize-browser-retirement-integration` matches `prd.branchName`.
- admitted accepted baseline is `d4766c3dbbf2c198142047ead4449d58dd47d485`; fetched origin/main `3963bc3566da24f8214634c17a9d0f79a6724171` is an ancestor of this isolated branch.
- C61 `8e8177c031a7b3b9322d712af19970e13fa7a1bc` and C83 `22cc6769aaf06d1e2c1275b064cc7ec29de3e371` candidate refs remained unchanged.
- the expected clean C61/C83 predecessor worktrees were present at their pinned heads before and after both analyzer orders.
- current source bindings are runner `f2bdc990fc710f8baddb9766837f101a0c08f32d0386cc29117a826a08a83cef`, verifier `7e3873f7cc54a473794ce0378faad2e22dde2b226b909497cc4c8f558cecc07d`, and negative fixture `0f51a143cf809d20df04bbf4350473a14961979f7ffc854eff1f7fea6b9b3840`.

Merge-order and causal evidence:

- required analyzer: `main -> C61 -> C83`, passed; candidate refs and preserved worktrees unchanged.
- reverse control analyzer: `main -> C83 -> C61`, passed; candidate refs and preserved worktrees unchanged.
- `verify.py --mode merge-orders` passed source-driven provenance/ledger and both merge rehearsals.
- `verify.py --mode merge-orders` and `verify.py --mode all --write runs/final/verification.json` passed. The all-mode checks include provenance/scope, merge orders, behavior/attribution, handoff sequence, determinism, negative fixtures, caller-tree mutation control, and public checks.
- all report negatives and public negatives rejected as required; the live caller-tree mutation control was also rejected as required.
- the manifest contains nine broad gates, all `OPEN`; C61, C83, project acceptance, review, merge, and vertical-probe claims remain false.

## `browser-audio-tool`

Command:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/run_public_checks.py --case browser-audio-tool --child-timeout 90 --aggregate-timeout 300 --output /tmp/c109-repaired-browser-90-300-v2.json
```

Result: `passed`; elapsed `267.248s`; child bound `90s`, aggregate bound
`300s`; final public report SHA-256
`a468ad0ba3ce586035f54bff3d2bdb1d84e581d4f7f42a1dcb7ba32e56d5fc92`.

All 18 checks passed with exit code 0: browserrunner/browserscenario normal
and race; C61 and C83 external consumers normal/race with `GOWORK=off`; the
accepted-main delta barrier regression; CLI cancellation normal/race;
nomicrophone cross-module coverpkg cancellation; accumulated normal, coverage,
and split race transport/integration/devices/OpenAI regressions; and the
shipped credential-free browser/audio/tool workflow. Discovery was positive
for every Go/script check, output remained uncapped and hash-matched, and the
synthetic tree stayed unchanged. The shipped report classified effects as
`SOFTWARE_LOCAL_PROCESS_ONLY`.

## `malformed-or-canceled`

Command:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/run_public_checks.py --case malformed-or-canceled --child-timeout 60 --aggregate-timeout 180 --output /tmp/c109-repaired-malformed-final.json
```

Result: `passed`; elapsed `34.582s`; child bound `60s`, aggregate bound
`180s`; final public report SHA-256
`e0b8b634fe34dc76e996fbdeacd01a1fc6d4e34d2cc9dd4fb4e32f3f8d26e5cd`.

All three checks passed with exit code 0: malformed credential overflow,
timeout and cleanup; canceled browser scenario normal; and canceled browser
scenario race. The negative control was software-only and had no external
browser or microphone effect.

## Caller-owned `--tree` and mutation controls

Command:

```text
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c109-characterize-browser-retirement-integration/run_public_checks.py --case malformed-or-canceled --child-timeout 60 --aggregate-timeout 180 --tree /tmp/c109-caller-tree-current --output /tmp/c109-public-malformed-caller-tree.json
```

Result: `passed`; the caller-owned tree was preserved with clean head/tree/
status, while the runner's auxiliary temporary synthetic tree was removed
successfully. A child that intentionally created an untracked marker in a
caller-owned tree was rejected as required. A clean C61-only tree was also
rejected by the synthetic ancestry checks; arbitrary trees cannot be used as
public evidence.

The final evidence remains handoff-only: no CI-green, review, merge, vertical
acceptance, hardware, acoustic, or project-acceptance claim is made. C61 and
C83 remain unmerged and unaccepted pending their own task/review/CI gates.
