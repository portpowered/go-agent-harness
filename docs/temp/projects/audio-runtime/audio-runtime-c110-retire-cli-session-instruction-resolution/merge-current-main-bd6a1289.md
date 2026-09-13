# C110 current-main integration checkpoint

- Admission remains the sole `audio-runtime` / `audio-runtime-v1` project, and
  the task branch matches `prd.json.branchName`.
- `origin/main` was fetched at
  `bd6a1289218d1bef1a3af36e64e9d4496062416f` and merged into the preserved clean
  C110 branch as `8f84e05d3975fc3619f24f18cbe71270c6356312`. The running host
  checkout and peer worktrees were not reset or merged.
- C79 `work-task-25` and `work-review-31` are terminal-complete; its guarded
  merge is present in current main and the shared registry lease is released.
- The C110 source repair is checkpointed and pushed at `961608da1`: policy
  implementation is private under `sessioninstructions/internal/service`, the
  public package has no construction factory, and the session Wire no longer
  imports a peer Wire; CLI construction callers use the dedicated instruction
  Wire directly.
- Bounded evidence after the repair: sessioninstructions normal tests passed
  (18 tests across 3 packages), the private service race selection passed (54
  tests across 3 packages), the focused CLI instruction suite passed (223
  tests), the GOWORK=off positive/invalid consumer and both mutation guards
  passed, `make wire-check` passed, and `make architecture-size-check` passed
  after the composition-boundary repair.
- The executable-input checkpoint is `29e8ff57a76ca4bf02f30bbdab829dc274634846`.
  The bounded `run.py` evidence run
  `run-20260913T131916Z-14414` accepted the instruction/text-seed positive,
  invalid pre-provider negative, clean shutdown, and C16 audio/tool replay.
  Its YUI artifact SHA-256 is
  `40fd2c92fa56ad58b572edceee84c6d73a24e43efca045071d392c632de4a531`;
  provenance, artifact, and summary all bind to that source revision.
- Accumulated normal, coverage, and race session regressions passed at the
  preserved count of three. Full vet, pinned staticcheck 2026.1, pinned
  golangci-lint 2.9.0 (0 issues), format, coverage registration, Wire, and
  architecture-size gates passed. This remains implementation handoff
  evidence; CI, independent review, guarded merge, and project acceptance
  remain external.
  Full provenance, artifact and public replay evidence must be regenerated from
  the final source head before script-CI handoff.
