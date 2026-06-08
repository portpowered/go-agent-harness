# Factory Overview

This factory coordinates autonomous work for the AI model reference website.
The ideafy workstation is the meta-planner. It chooses phase-scoped batches,
submits ideas, and records progress. Executors implement PRD stories in
worktrees. Review gates the resulting PRs.

## Read First

Before submitting work, read:

* `factory/factory.json`
* `factory/workstations/ideafy/AGENTS.md`
* `docs/internal/customer-ask.md`
* `docs/internal/checklist.md`
* `docs/internal/progress.txt`
* `docs/documentation-site-pages-needed.md`
* `you docs agents`
* `you docs batch-inputs`

## Phase Control

Current phase authorization lives in:

```txt
docs/internal/customer-ask.md
```

The meta-planner may dry-run batches during planning. It must not submit a real
batch unless `customer-ask.md` sets `realSubmissionAuthorized: true` or the
customer explicitly authorizes submission in the current conversation.

Phase work is review-gated through Phase 10. After Phase 10, long-tail backfill
may run mostly autonomously in small batches, still with batch summaries and
review.

## Work Types

Configured work types:

```txt
thoughts       meta-planner loopback work
idea           product/implementation idea submitted by ideafy
plan           PRD planning output from an idea
task           executor/review implementation work
cron-triggers  runtime trigger type
```

Use `idea`, singular, for implementation proposals.
Use `thoughts`, plural, for ideafy loopback.

## Workstation Flow

```txt
thoughts:init -> ideafy -> thoughts:complete

idea:init -> plan -> idea:to-complete + plan:init
plan:init -> setup-workspace -> plan:complete + task:init
task:init -> process -> task:in-review
task:in-review -> review -> task:to-complete
idea:to-complete + task:to-complete with the same name -> consume
```

## Batch Submission

Use the canonical `FACTORY_REQUEST_BATCH` shape from `you docs batch-inputs`.

For a running factory, prefer:

```sh
you submit batch <path>
```

Always dry-run first:

```sh
you submit batch --dry-run <path>
```

For watched-folder operator ingress, use:

```txt
factory/inputs/BATCH/default/<request_id>.json
```

The checked-in example is:

```txt
factory/docs/batch-input-example.json
```

## State Inspection

Use:

```sh
you work list
you session list
```

`you work list` shows durable work state. `you session list` shows active or
recent runtime sessions. Check both before deciding that work is stuck or before
submitting a new batch.

## Repair

Use:

```sh
you work move
```

only for deliberate workflow repair. Record every manual move in
`docs/internal/progress.txt` with the work item, old state, new state, reason,
and expected next workstation. Do not use work moves to skip implementation,
review, or validation.

## Local State Files

```txt
docs/internal/customer-ask.md  current phase and submission authorization
docs/internal/checklist.md     high-level phase and customer ask tracking
docs/internal/progress.txt     append-only meta-planner progress log
```

## Setup Workspace Ownership Contract

`setup-workspace` owns only per-work-item worktree resolution: it reads the PRD,
chooses the branch/worktree path, creates or reuses that worktree, and copies
the task artifacts into the ready checkout.

It does not own routine root-checkout `git pull` or root `git worktree prune`.
Those are shared-root maintenance operations, so running them inside a
high-frequency concurrent setup path creates avoidable contention against other
setup runs and planner-owned root state.

This ownership split is the current factory contract for `CTRL-FAC-01` and
`CTRL-FAC-02`. It preserves the observable setup outcome of returning a ready
worktree for the requested PRD branch while removing the shared-root mutation
that previously stranded `plan:init` work on setup races such as
`fatal: Cannot rebase onto multiple branches.`.

When overlapping setup runs target the same PRD branch, the losing setup run
should observe the registered worktree created by the winner and return that
same ready path as a reuse instead of retrying shared-root sync or failing the
queue item. Setup still fails explicitly when the target path resolves to a
different registered branch or another unsafe worktree state.

Planner-owned root dirtiness is part of that contract. Routine changes in
`docs/internal/checklist.md` and `docs/internal/progress.txt` are tolerated
during setup and reuse because they are operational planner state, not
worktree-selection inputs. The requested `tasks/todo/<prd-name>.json` and
optional `.md` are also tolerated as direct setup inputs. Other root dirty
state still fails explicitly so the factory does not silently treat unrelated
checkout drift as safe.

## Verification

Reviewers can verify the repaired setup path with committed runtime coverage:

```sh
make test-factory-scripts
make typecheck
make test
```

`make test-factory-scripts` runs the setup-workspace runtime suite with
`PYTHONDONTWRITEBYTECODE=1` and `python3 -B` so the verification path does not
write `__pycache__` artifacts into the root checkout.

The Python runtime suite covers the queue-facing setup contract directly:

- setup does not issue shared-root `git pull` or `git worktree prune`
- overlapping setup runs reuse the winner's registered worktree instead of
  stranding `plan:init` on `fatal: Cannot rebase onto multiple branches.`
- planner-owned dirty state in `docs/internal/checklist.md` and
  `docs/internal/progress.txt` is tolerated for both first-time setup and
  worktree reuse
- unrelated root dirtiness still fails with an explicit unsafe-state error

## Resolved Symptoms

This repair addresses the currently observed queue symptoms:

- concurrent or repeated `setup-workspace` runs no longer compete on shared
  root `git pull` / `git worktree prune` behavior, so new `plan:init` work does
  not fail with the reproduced rebase race
- planner-owned dirty root state in `docs/internal/checklist.md` and
  `docs/internal/progress.txt` no longer blocks ready-worktree creation or reuse

This repair does not auto-repair queue items that were already stranded before
the code fix landed. Any existing stuck tokens still require deliberate
operator follow-up with `you work list`, `you session list`, and, if needed,
manual `you work move` repair recorded in `docs/internal/progress.txt`.
