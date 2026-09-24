# C109 aggregate-budget runner repair — 2026-09-13

This checkpoint changes only the admitted C109 evidence directory. The
preserved C61/C83 refs and worktrees, production and test source, C79 shared
registries, the running host checkout, and unrelated owner paths were not
changed.

Admission and current-main integration:

- `project-control.py verify-work --type task --name
  audio-runtime-c109-characterize-browser-retirement-integration --root
  "$FACTORY_ROOT"` returned `admitted` for the sole `audio-runtime` project.
- The isolated branch remains
  `codex/audio-runtime-c109-characterize-browser-retirement-integration`,
  exactly matching `prd.json.branchName`.
- `git fetch origin main` advanced review-time `origin/main` to
  `ea53be13ce5e4ef14fd8c89c695c21744a1f7686` (C107 merge). It was integrated
  into this isolated worktree as merge commit
  `629132f33a584dbf026c3a33296cd5d97958f031`; accepted main
  `d4766c3dbbf2c198142047ead4449d58dd47d485`, startup integration
  `8bdafc7f947a3a2c9856220abdc539437035bd21`, and both preserved candidate
  revisions remain ancestors.
- Required and reverse analyzers both pass against accepted main with C61
  `8e8177c031a7b3b9322d712af19970e13fa7a1bc` followed by C83
  `22cc6769aaf06d1e2c1275b064cc7ec29de3e371` as the only promoted order. Both
  report unchanged candidate refs and preserved worktrees.

Cause and repair:

- A fresh positive public run using the previous runner passed 17/18 checks.
  The 18th check, the shipped credential-free browser/audio/tool workflow,
  was started last with four seconds remaining in the 300-second aggregate and
  was terminated at the bound. The prior 17 checks passed, cleanup succeeded,
  and no process group survived. This is a deterministic scheduling diagnosis:
  the required workflow was unnecessarily exposed to accumulated-test budget
  consumption.
- `34a4a9404e1238b840c83c7acf7b93c14fa1abca` moves that unchanged shipped
  command to the first position in `command_set("browser-audio-tool")`. It
  does not alter the command, test patterns, assertions, child limit,
  aggregate limit, credential policy, or cleanup behavior.

Fresh causal evidence from the repaired runner:

- `test_analyze.py -v`: 2/2 analyzer package-resolution regressions pass.
- Positive public report: status `passed`, 18/18 checks, aggregate elapsed
  `265.785s` under `--child-timeout 90 --aggregate-timeout 300`; the shipped
  workflow exits 0 and records `PROBE_TOOL_MARKER_9182` plus
  `strict replay continuation`. All children are credential-free and all
  process groups are clean.
- Malformed/canceled public report: status `passed`, 3/3 checks, aggregate
  elapsed `35.286s` under `--child-timeout 60 --aggregate-timeout 180`; the
  malformed negative and canceled normal/race controls retain positive test
  discovery and clean shutdown.
- `verify.py --mode all --write runs/final/verification.json`: passes
  provenance, merge-orders, behavior-and-attribution, handoff-sequence,
  determinism, negative-fixtures, caller-tree-mutation-control and
  public-checks, plus all 19 negative fixtures.
- Final evidence hashes: `verification.json`
  `5fd1b8844a3ce24f8eb66393141455cf039ebd52facf3a485442d29861c68ac0`;
  required `report.json`
  `cfeb38601fa5c7ee71f384cf37d06aea2fbf060b052687d9abefca4a45484d8`;
  control `report.json`
  `ba5defa349034d9c619e3638bbb3f365cbbe1e9b336f97b05adcd7f732860d44`;
  positive public report
  `579b6a0a1867907a973e877d527b8b0b88631fd82a4740c586de8cc5dd69ce29`;
  malformed/canceled report
  `8c8e2864abae789d1e7f47ea1725bf252e8324f3afcb60126408dba3e0970848`.

The latest hosted rejection remains preserved in
`ci-rejection-34737204831.json`: coverage failed in unchanged WebMCP TempDir
cleanup and hermetic failed in unchanged session-cancellation isolation; both
targeted local controls pass, and neither is a C109-owned repair. The C79
provider-audio terminal-drain loss remains separately assigned and unwaived.
No CI-green, review, merge, vertical probe, C61/C83 acceptance, hardware or
acoustic claim is made. Next action: commit and push this same task/PR, update
PR #495 with the repair and evidence, and return `ACCEPTED` to script CI without
polling; retain ownership for any exact in-scope rejection.
