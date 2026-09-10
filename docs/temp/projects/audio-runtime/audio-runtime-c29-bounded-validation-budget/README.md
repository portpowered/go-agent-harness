# C29 bounded validation budget evidence

Work `audio-runtime-c29-bounded-validation-budget` is admitted to the single
`audio-runtime` project. The isolated branch is
`codex/audio-runtime-c29-bounded-validation-budget`.

## Admission and ancestry

- `rtk proxy python3 "$FACTORY_ROOT/factory/scripts/project-control.py" verify-work --type task --name audio-runtime-c29-bounded-validation-budget`
  returned `{"status": "admitted", "project": "audio-runtime", "name": "audio-runtime-c29-bounded-validation-budget"}`.
- `prd.json` branchName matches the isolated worktree branch.
- Baseline: `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad`.
- Startup integration: `8bdafc7f947a3a2c9856220abdc539437035bd21`.
- Freshly fetched `origin/main`: `9a115a83435933b4e5558ee60613fabc963afcd6`.
- All three revisions are ancestors of the candidate worktree. No host checkout
  was merged, reset, or used for admission mutation.

The complete canonical board was saved as `/tmp/audio-runtime-c29-board.json`.
It contains the C29 idea, plan, and task rows; the task is `init`, with no C29
review row and no C29 CI rejection feedback. No prior C29 finding is waived.
The inherited `progress.txt` contains predecessor C07/C21 checkpoints and was
preserved unchanged.

## Causal finding and repair

The pre-fix public script required `budget.timeSeconds == 1800`, causing a
valid authorized 900-second packet to fail before launch with the exact
diagnostic `mission requires a 1800-second budget and description`.

`factory/scripts/prepare-validation.py` now accepts only JSON integers in
`[1, 1800]`, explicitly excluding bool, rejects non-object/missing budgets,
preserves the supplied integer, and leaves description, Realtime, identity,
criterion, authority, artifact, report, staging, admission, and cleanup checks
unchanged.

The byte-identical pre-fix source is
`prepare-validation-before.py` with SHA256
`f7d63136f729af91a32dceebd05f9e15c2f9fbf08edaef14602e1f04b989b92c`. The
repaired source hash before commit was
`ef66c785dbf342640ab4a02ec993b0ef5ff3d95848a2685a2da39113c3164101`.

## Evidence

- `public-controls-legacy.json`: real subprocess reproduction of pre-fix 900
  rejection and pre-fix 1800 success.
- `public-controls-bounded.json`: real subprocess matrix for 1/900/1800,
  malformed and out-of-range budgets, project/vertical scope, admission and
  authority identity, artifact/report/realtime/criterion protections, staging
  modes and digests, and equality/coercion mutation controls.
- `test_prepare_validation_budget.py`: four focused tests, each child process
  bounded to 10 seconds.
- `run-focused.py`: records a focused command with a 120-second outer
  `subprocess.run(timeout=120)` deadline.

Commands and outcomes:

```text
rtk proxy python3 -m unittest discover -s factory/scripts/tests -p test_prepare_validation_budget.py -> 4 tests, OK
rtk proxy python3 -m unittest discover -s factory/scripts/tests -p test_project_control.py -> 12 tests, OK
rtk proxy python3 -m unittest discover -s factory/scripts/tests -p test_project_admission.py -> 11 tests, OK
rtk proxy python3 -m unittest discover -s factory/scripts/tests -p test_factory_graph.py -> 2 tests, OK
rtk proxy python3 -m unittest discover -s factory/scripts/tests -p test_factory_script_target.py -> 4 tests, OK
rtk proxy python3 public-controls.py --script prepare-validation-before.py --expect legacy --child-timeout-seconds 10 -> passed
rtk proxy python3 public-controls.py --script factory/scripts/prepare-validation.py --expect bounded --child-timeout-seconds 10 -> passed
rtk proxy git diff --check -> passed
```

No script CI, independent review, merge, post-merge probe, operator deployment,
C28 acceptance, or project acceptance is claimed. The immutable project gates
remain open as recorded in `prd.json`.

## Handoff

Before `ACCEPTED`, commit and push only the owned source, focused tests, and
owned evidence; open/update the task PR with the exact candidate SHA and this
ledger. Return `ACCEPTED` to the script-owned current-head CI gate without
polling it. An exact CI rejection remains same-task executor work through
`CONTINUE`; no unchanged resubmission is allowed. After reviewed merge, meta
must run the fresh immutable vertical preparation probe and same-source CLI
regression, then decide safe deployment at a no-preparation-active checkpoint.
