# C109 current-head CI rejection characterization — 2026-09-13

This checkpoint changes only the admitted C109 evidence directory.  The
preserved C61/C83 refs and worktrees, production and test source, C79 shared
registries, the running host checkout, and unrelated owner paths were not
changed.

Admission and baseline identity:

- `project-control.py verify-work --type task --name
  audio-runtime-c109-characterize-browser-retirement-integration --root
  "$FACTORY_ROOT"` returned `admitted` for the sole `audio-runtime` project.
- The isolated branch is
  `codex/audio-runtime-c109-characterize-browser-retirement-integration`,
  exactly matching `prd.json.branchName`.
- `git fetch origin main pull/452/head:c109-c61-readonly
  pull/473/head:c109-c83-readonly` completed successfully.  `origin/main` is
  `b7d25ca6f0e9b94c62b193059160dfbf446ef1d6`, already an ancestor of the
  candidate `0c31a7b01581a1fed95fecabed97558762d3e32a`; accepted main
  `d4766c3dbbf2c198142047ead4449d58dd47d485` and startup integration revision
  `8bdafc7f947a3a2c9856220abdc539437035bd21` remain ancestors.
- The immutable C61/C83 refs remain
  `8e8177c031a7b3b9322d712af19970e13fa7a1bc` and
  `22cc6769aaf06d1e2c1275b064cc7ec29de3e371`.  The branch diff against
  `origin/main` contains only the C109 evidence directory.

Owned evidence revalidation:

- `test_analyze.py -v` passed its two package-resolution regressions.
- `verify.py --mode all --write /tmp/c109-verification-0c31.json` passed all
  eight accumulated checks and all 20 negative fixtures, including stale and
  reversed inputs, exact source-derived caller/API/conflict evidence, public
  tree binding, cleanup, test discovery, attribution, and the caller-tree
  mutation control.
- The admitted positive public command passed all 18 checks under the exact
  `--child-timeout 90 --aggregate-timeout 300` bounds.  The malformed/canceled
  command passed all three checks under `60/180` bounds.  Both reports had
  credential-free output, positive required-test discovery, bounded cleanup,
  and no surviving process groups.  Their complete temporary report hashes
  are retained in `ci-rejection-34737204831.json`.

Current-head CI rejection:

- Completed PR #495 run `34737204831` tested candidate head
  `0c31a7b01581a1fed95fecabed97558762d3e32a` at merge checkout
  `085d5bc7ba037d3856d7885943f2f6e90f43ae6a`.  Seven lanes passed; coverage
  and hermetic failed.  The complete downloaded logs were read and their
  hashes are recorded in `ci-rejection-34737204831.json`.
- Coverage failed in the unchanged WebMCP lifecycle test because Go's
  `TempDir` cleanup observed a non-empty `browser-profile` directory.  The
  exact test passes 10/10 under the same `nomicrophone` and coverage topology
  locally.  Hermetic failed in the unchanged session cancellation isolation
  test after its 2-minute bound; the exact test passes locally in 4.253s.
- Neither failure names a C109 path, and `git diff origin/main...HEAD` is empty
  for both failing source packages.  C109 therefore makes no out-of-lease
  production/test repair, does not weaken timing or cleanup assertions, and
  does not relabel either failure as fixed or waived.  The precise external
  ownership and local controls are in the JSON record.

Handoff state:

- C109's evidence checks are green, but current-head script CI is not green;
  no candidate acceptance, review, merge, vertical probe, hardware/acoustic
  proof, or project acceptance is claimed.  C61 and C83 remain unmerged and
  unaccepted, and all nine broad project gates remain `OPEN`.
- Exact next action: preserve this changed evidence checkpoint, commit and
  push it on the same PR #495, update the PR with the two external failure
  signatures and routing, and retain the same C109 task until the script gate
  can accept a candidate.  Do not modify peer ownership or resubmit an
  unchanged candidate merely to retry these failures.
