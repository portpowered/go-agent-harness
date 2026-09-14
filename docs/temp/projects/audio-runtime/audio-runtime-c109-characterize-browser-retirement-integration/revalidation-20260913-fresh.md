# C109 fresh executor revalidation — 2026-09-13T07:35:55Z

This checkpoint changes only the admitted C109 evidence directory. The
preserved C61/C83 refs and worktrees, production and test source, C79 shared
registries, the running host checkout, and unrelated owner paths were not
changed.

Admission and identity:

- `project-control.py verify-work --type task --name
  audio-runtime-c109-characterize-browser-retirement-integration --root
  "$FACTORY_ROOT"` returned `admitted` for the sole `audio-runtime` project.
- The branch is
  `codex/audio-runtime-c109-characterize-browser-retirement-integration`,
  matching `prd.json.branchName`; executor HEAD is
  `95e0a2c4e242aef8e2c6e192a4719ae29a8d9ac3`.
- `git fetch origin main pull/452/head:c109-c61-readonly
  pull/473/head:c109-c83-readonly` completed. The review-time main is
  `ea53be13ce5e4ef14fd8c89c695c21744a1f7686`, and the read-only candidate refs
  remain C61 `8e8177c031a7b3b9322d712af19970e13fa7a1bc` and C83
  `22cc6769aaf06d1e2c1275b064cc7ec29de3e371`.
- Accepted main `d4766c3dbbf2c198142047ead4449d58dd47d485` and startup
  integration revision `8bdafc7f947a3a2c9856220abdc539437035bd21` remain
  ancestors. The diff against review-time `origin/main` contains only the
  C109 evidence directory.

Owned causal checks:

- `test_analyze.py -v` passed both package/module-resolution regressions.
- `verify.py --mode all --write /tmp/audio-runtime-c109-verification-95e0.json`
  passed all eight accumulated checks and all 19 negative fixtures, including
  deterministic merge-order evidence, source-derived caller/API/conflict
  evidence, handoff sequencing, public-tree binding and caller-tree mutation
  rejection. The temporary verification output SHA-256 is
  `5fd1b8844a3ce24f8eb66393141455cf039ebd52facf3a485442d29861c68ac0`.
- The exact positive command
  `run_public_checks.py --case browser-audio-tool --child-timeout 90
  --aggregate-timeout 300` passed 18/18 in `256.463s`. The synthetic tree was
  main → C61 → C83 with tree hash
  `4d8e93ae0e74db403fdb02c70db9cf849c590156` and temporary HEAD
  `bb18860cfae386fef93b1ea2717075e3570d4c35`; all children were within the
  90-second bound, output was uncapped and credential-free, and temporary
  worktree cleanup passed. The complete temporary report SHA-256 is
  `1359e5b94c91b22b1c62b3c5713277af105fe289cf8541cd57c6a59fcb8f5eec`.
- The exact negative command
  `run_public_checks.py --case malformed-or-canceled --child-timeout 60
  --aggregate-timeout 180` passed 3/3 in `38.039s`; malformed
  credential/overflow/timeout cleanup and canceled browser scenario normal/race
  each retained positive discovery, credential-free output and clean process
  groups. Temporary worktree cleanup passed. The complete temporary report
  SHA-256 is
  `ab0bc01961718cfa461b801ab77007ed40f8330c010f83e3abb1e4bbab0eff63`.
- The positive run discovered three browserrunner/browserscenario normal tests,
  three under race, both external consumers under normal/race, the exact
  nomicrophone cross-module coverpkg cancellation control, and all accumulated
  normal/coverage/race regression groups. The accepted-main delta barrier
  regression passed three repetitions.

Preservation and failure routing:

- C61 and C83 worktrees are clean at their pinned heads. The C109 worktree is
  clean after the evidence checkpoint, and no production, test, Wire-registry,
  architecture-baseline or peer evidence path was changed.
- The latest hosted rejection remains the exact run `34737204831`: unchanged
  WebMCP TempDir cleanup and agent-loop session-cancellation isolation failures.
  Targeted local controls pass, but these findings remain outside C109's
  evidence-only lease and are neither repaired, waived nor relabeled here.
  The separate 6,400-sample provider-audio terminal-drain loss remains assigned
  to C79 and is not duplicated.
- This is executor evidence only. No CI-green, C61/C83 fixed/merged/probed or
  accepted, hardware/acoustic, or project-acceptance claim is made. The next
  action is to commit/push this owned checkpoint and submit the exact same task
  head to script CI without agent polling; independent review and guarded merge
  remain external.
