# Phase 2 Factory Worktree Hygiene Convergence Report

## Subject Under Review

- Validator branch: `phase-2-factory-worktree-hygiene-validator`
- Repaired branch under review: `phase-2-factory-worktree-hygiene-repair`
- Report scope completed in this iteration: checklist convergence against
  `CTRL-FAC-01` through `CTRL-FAC-03` and repaired `setup-workspace` behavior

This report validates the delivered repository state for the completed
`phase-2-factory-worktree-hygiene-repair` slice. It stays scoped to factory
queue convergence and operational setup behavior rather than broad duplicate CI
review.

## Checklist Convergence Findings

| group | subject | outcome | evidence | affectedFilesOrSurfaces | requiredFollowUp |
| --- | --- | --- | --- | --- | --- |
| `checklist-convergence` | `CTRL-FAC-01` root sync ownership stays outside `setup-workspace` | `pass` | `factory/scripts/setup-workspace.py` contains no `git pull` invocation and limits setup behavior to repository discovery, root dirty-state validation, worktree registration checks, worktree creation or reuse, and task artifact copying. `factory/docs/overview.md` now names root-checkout `git pull` as shared-root maintenance outside the setup hot path and explains that concurrent setup must not mutate the shared root during ready-worktree resolution. The committed runtime test `test_setup_workspace_skips_root_pull_and_prune` wraps `git`, exercises `setup-workspace`, and proves the script does not execute `pull` or `pull --ff-only` while still returning `status=ready` for the requested branch. | `docs/internal/checklist.md`, `factory/scripts/setup-workspace.py`, `factory/docs/overview.md`, `factory/scripts/tests/test_setup_workspace.py` | None. |
| `checklist-convergence` | `CTRL-FAC-02` worktree maintenance ownership stays outside `setup-workspace` | `pass` | `factory/scripts/setup-workspace.py` does not invoke `git worktree prune`; instead it restricts itself to `git worktree list` for registration checks and `git worktree add` for the requested branch. `factory/docs/overview.md` explicitly states that routine root `git worktree prune` is not owned by setup and ties the previous shared-root mutation to stranded `plan:init` queue symptoms. The same wrapped-git runtime test `test_setup_workspace_skips_root_pull_and_prune` records executed git commands and proves no `worktree prune` command runs during setup. | `docs/internal/checklist.md`, `factory/scripts/setup-workspace.py`, `factory/docs/overview.md`, `factory/scripts/tests/test_setup_workspace.py`, `prd.md` | None. |
| `checklist-convergence` | `CTRL-FAC-03` planner-owned dirty root tolerance with explicit unsafe-state failure for everything else | `pass` | `factory/scripts/setup-workspace.py` defines `PLANNER_OWNED_DIRTY_PATHS` as `docs/internal/checklist.md` and `docs/internal/progress.txt`, tolerates those paths plus the requested `tasks/todo/<prd-name>.json` and optional `.md`, and raises a direct runtime error when any other dirty root state appears. `factory/docs/overview.md` describes the same contract. The committed runtime tests prove both halves of the rule: `test_setup_workspace_allows_planner_owned_dirty_root_files` and `test_setup_workspace_reuses_existing_worktree_with_planner_owned_dirty_root_files` return `status=ready`, while `test_setup_workspace_fails_for_non_planner_owned_dirty_root_state` fails with an explicit unsupported-dirty-state error that includes the offending `git status` entry. | `docs/internal/checklist.md`, `factory/scripts/setup-workspace.py`, `factory/docs/overview.md`, `factory/scripts/tests/test_setup_workspace.py` | None. |

## Checklist Convergence Verdict

Checklist convergence currently passes for `CTRL-FAC-01`, `CTRL-FAC-02`, and
`CTRL-FAC-03`. The repaired setup contract is now stated consistently in the
checklist, factory overview, runtime script, and deterministic runtime tests,
and the cited checklist rows can be mapped directly to observable repository
behavior instead of prior batch history.

## Repaired Setup-Workspace Behavior Findings

| group | subject | outcome | evidence | affectedFilesOrSurfaces | requiredFollowUp |
| --- | --- | --- | --- | --- | --- |
| `setup-workspace-behavior` | Previously reproduced concurrent setup race for the same PRD branch | `pass` | The repaired code path now treats overlapping setup as a worktree-registration race rather than a shared-root sync race. `create_or_reuse_worktree(...)` first waits briefly for a concurrently created reusable worktree, then rechecks the registered branch and returns reuse when another setup run already created the expected worktree. The committed runtime test `test_setup_workspace_recovers_when_parallel_runs_race_on_worktree_creation` deterministically forces the first `git worktree add` to sleep, launches a second `setup-workspace` run against the same PRD branch `phase-2-factory-worktree-hygiene-repair`, and verifies that both runs exit `0` with `status=ready`, the same `worktree` path, the same `branch`, and complementary `reused` values `{False, True}`. That evidence directly supports the conclusion that the earlier concurrent setup failure is fixed on the repaired code path rather than still reproducing. Supporting deterministic coverage in the same suite also proves the repaired path keeps setup scoped correctly: no root `pull` or `worktree prune`, planner-owned dirty-root reuse remains allowed, and unrelated root dirt still fails explicitly instead of being silently reused. | `factory/scripts/setup-workspace.py`, `factory/scripts/tests/test_setup_workspace.py`, `factory/docs/overview.md`, requested branch `phase-2-factory-worktree-hygiene-repair` | None. |

## Repaired Setup-Workspace Verdict

The previously reproduced concurrent `setup-workspace` failure is fixed on the
repaired code path based on deterministic direct exercise of overlapping setup
runs for the same PRD branch. Current repository state does not show a
remaining mismatch between the repaired runtime behavior and `CTRL-FAC-01`
through `CTRL-FAC-03`.

## Durable-Queue Recovery Status

| group | subject | outcome | evidence | affectedFilesOrSurfaces | requiredFollowUp |
| --- | --- | --- | --- | --- | --- |
| `durable-queue-recovery` | Stranded `plan`, `task`, or `thoughts` token inspection after the repair-focused setup validation | `pass` | Direct queue inspection now shows no stranded `plan`, `task`, or `thoughts` items attributable to the repaired worktree hygiene failure mode. `you session list` identifies the live project session `e7dc6d48-caed-4de0-9868-fa66ee4823ea` for `/Users/abdifamily/work/go-agent-harness`, and `you work list --session e7dc6d48-caed-4de0-9868-fa66ee4823ea` shows the repaired work item `phase-2-factory-worktree-hygiene-repair` as `idea/complete`, `task/complete`, and `plan/complete`. The only non-terminal worktree-hygiene entries are the current validator lane itself: `batch-phase-2-factory-worktree-hygiene-repair-batch-009-phase-2-factory-worktree-hygiene-validator` in `idea/to-complete`, `batch-request-022e46e44397f62845e5bd173c56b30a-phase-2-factory-worktree-hygiene-validator` in `task/init`, and `batch-phase-2-factory-worktree-hygiene-repair-batch-009-ideafy-loopback-phase-2-factory-worktree-hygiene-repair-batch-009` in `thoughts/init` with explicit dependency edges requiring both the repair idea and this validator idea to complete. Those states are active workflow dependencies, not queue damage. No `phase-2-factory-worktree-hygiene-*` work item appears in `failed`, and no exact manual recovery move is needed for this slice. An unrelated failed `work-plan-3 phase-2-constructor-ownership-boundaries` item exists in the same session, but it is outside the repaired worktree-hygiene names and therefore not evidence of remaining queue fallout from this failure mode. | durable queue state for session `e7dc6d48-caed-4de0-9868-fa66ee4823ea`, `you session list`, `you work list --session e7dc6d48-caed-4de0-9868-fa66ee4823ea`, `factory/docs/overview.md` | No manual `you work move` recovery is required for `phase-2-factory-worktree-hygiene-repair` based on the observed queue state. |

## Current Overall Convergence Verdict

Current overall verdict for `phase-2-factory-worktree-hygiene-repair` remains
temporarily `uncertain` in this in-progress report.

The repaired `setup-workspace` behavior and the checklist controls
`CTRL-FAC-01` through `CTRL-FAC-03` now pass on observable repository evidence.
Durable-queue inspection now also passes and records that no manual
`you work move` recovery remains for the repaired slice. The remaining work on
this branch is the publication step in story
`phase-2-factory-worktree-hygiene-validator-004`, which will collapse these
completed findings into the final reviewer-facing verdict.
