# audio-runtime-c10-preserve-rename-validation evidence ledger

Task: `audio-runtime-c10-preserve-rename-validation`.
Project: admitted `audio-runtime`; factory session `~default`; server
`http://127.0.0.1:7439`.

## Admission, branch, and preserved ancestry

- Read `factory/docs/operating-policy.md`,
  `factory/docs/implementation-handoff.md`,
  `factory/docs/c09-operator-findings.md`, the current meta-status, the
  complete C09 vertical report, the immutable project manifest and source plan,
  `prd.json`, and `progress.txt`.
- Canonical board JSON was saved without truncation at
  `/tmp/audio-runtime-c10-work-list.json`. It records task `work-task-7`, the
  concluded C09 review rows `work-review-9`, `work-review-10`, and
  `work-review-1`, and preserved C08 task `work-task-4`. All rejection feedback
  was extracted in full before implementation.
- Admission command:
  `rtk proxy python3 factory/scripts/project-control.py verify-work --type task --name audio-runtime-c10-preserve-rename-validation`
- Admission result: `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c10-preserve-rename-validation"}`.
- `prd.json.branchName` and the isolated worktree branch are both
  `codex/audio-runtime-c10-preserve-rename-validation`.
- `git fetch origin main` completed before implementation. Candidate commit
  `b918ebad54bdc9da660b7cd39739be6cc68b848a` is based on fetched
  `origin/main=bd1b37b02cdaaf1954a121a050641c7145349879` and retains startup
  integration `8bdafc7f947a3a2c9856220abdc539437035bd21` and baseline
  `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` as ancestors.
- C05, C06, C07, paused C08/PR400, merged C09/PR401, the parent checkout and
  factory configuration were not modified.

## Canonical findings and failure disposition

- `work-review-9` required exact-target lookup for persisted renames and
  historical version validation; `work-review-10` required one-to-one
  established migrations; `work-review-1` required bootstrap rename
  validation. Those defects are already repaired in the merged C09 commits
  `94aeda8b` and `e2533e0f` and remain covered by the C10 17-control runner.
- The independent C09 report at source `bd1b37b02cdaaf1954a121a050641c7145349879`
  reproduced the new defect: established and bootstrap missing-target public
  controls both exited `0` without `baseline-history-rename`.
- The latest recorded C09 CI rejection was read from the saved full logs
  `/tmp/factory-c09-coverage.log` and `/tmp/factory-c09-integration.log`.
  Coverage failed the unresolved-call `TestS2SV4DNeverReturningToolCallBoundedByExplicitTimeout` control; integration failed the fresh positive baseline of
  `TestSessionCLI_DuplexPCMMultiTurnRejectsLaterTurnCommitControls/missing_commit`
  after its existing two-second deadline. Neither failure is in C10-owned
  architecturegate paths; no timeout, assertion, baseline, or unrelated
  runtime code was changed, and the diagnostics remain preserved for the
  owning follow-up.

## Reproduction and repair

- Preserved C09 artifact-0 was verified at SHA-256
  `11663f4c2d749010bb352ea6e6f9d2605465878ba6a5ad49339c7aa4e288c00f`.
- Before repair, bounded public runs against the unchanged real-Git fixtures
  `missing-target-rename-fails` and `bootstrap-missing-target-fails` both
  exited `0` and emitted only inventory JSON. The fixture repositories were
  clean and their hashes were unchanged.
- `applyBaseline` now validates history against the complete loaded baseline
  before deriving check/module/package-scoped debt views. Scoped comparison
  still filters unrelated entries; global history metadata cannot disappear
  when a rename target is absent or outside the selected scope.
- `TestC10BaselinePublic` exercises the public composition path with a real Git
  history and a missing target under an architecture-scoped invocation.
- `history_controls.py` accepts `--binary`, `--fixtures`, and `--output`, uses
  bounded 120-second process groups, records command/cwd/exit/elapsed/stdout/
  stderr, and verifies preserved fixture hashes before and after execution.

## Causal validation

- Pre-repair runner: `evidence/before-run/summary.json` recorded 68 bounded
  public runs and correctly failed only the missing-target controls (including
  their scoped variants); no fixture was modified.
- Candidate gate binary SHA-256:
  `43ba0d70b0758ae7f96a33e5df9ad6f211207128a0f693283dbc747fc1a39c9e`.
- Post-repair runner: `evidence/after-run-final/summary.json` recorded all 17
  controls across `all`, explicit module/pattern, architecture, and size
  variants (68/68 passed). Both missing-target controls exited `1` with
  `baseline-history-rename`; fixture hashes were identical before and after.
- `GOWORK=off go test . -run "Test(C10BaselinePublic|Baseline)" -count=1 -timeout=3m`: passed.
- `GOWORK=off go test ./... -count=1 -timeout=3m`: passed.
- `GOWORK=off go test -race ./... -count=1 -timeout=3m`: passed.
- `make architecture-size-check`: passed with 181 packages, 1850 files, and
  26908 functions checked.
- `git diff --check`: passed. The architecture baseline and policy remain
  byte-identical with SHA-256 `a83cffb63edd8129b7573594ef12e275f66f4d54683db728347a1c45f581c6c5`
  and `d4e58a782f3daa11d7dedca420aaa78890a38305a1f7898a40fc3e08558494f3`.

## CI rejection repair

- Script-owned CI run `34205694843` at candidate head
  `8d3e4532801d62c12265c2ef1fbfe02bb49d7f92` passed formatting, Wire,
  architecture-size, vet, and staticcheck. Its exact `CI (static)` log showed
  golangci-lint `errcheck` failures for the two ignored `baselineJSON` errors
  in `tools/architecturegate/gate_test.go:509` and `:519`.
- The repair checks both serialization errors with `t.Fatal`. This initially
  put `gate_test.go` at 602 lines; removing two nonsemantic blank lines kept
  the test at the immutable 600-line limit. The focused test, full/race
  architecture tests, pinned local `make lint` (golangci-lint v2.9.0, zero
  issues), `make architecture-size-check`, public 68-run history replay, and
  `git diff --check` all pass after the repair.

No current hosted CI success is claimed; the script gate remains the owner of
broad required-check polling. The exact next action is commit and push this
same admitted branch, update its single C10 PR against current `main`, verify
the submitted head, and return `ACCEPTED` to the script-owned CI gate. Any
terminal rejection must return to this task for exact-log repair; independent
review, guarded merge, and the post-integration vertical probe remain external
stages.
