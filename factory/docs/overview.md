# Go agent harness factory

The checked-in `factory/factory.json` is the legacy delivery graph. It validates
with the installed `you factory config validate` command, but it is not yet the
single-project factory proposed for this migration. Configuration validity does
not prove admission, recovery, or acceptance behavior.

Read [the handoff plan](handoff-plan.md) for the pinned reference, target graph,
model assignments, stabilization gates, and remaining migration acceptance.

## Existing graph

```text
thoughts:init -> ideafy -> thoughts:complete
idea:init -> plan -> idea:to-complete + plan:init
plan:init -> setup-workspace -> plan:complete + task:init
task:init -> process -> task:in-review + review:init
task:in-review + review:init -> review -> task:to-complete + review:complete
idea:to-complete + same-name task:to-complete -> consume -> complete
```

The existing graph uses Sol medium for planning and ideation, Luna max for
implementation and review, and capacities of 16 for execution and planning.
It has no project work type, admission ownership, separate CI wait, project
reconciliation, or independent project validation stages. Those are migration
requirements, not behavior already delivered by this configuration.

Workspace preparation uses `factory/scripts/setup-workspace.py` and worktrees
under `.claude/worktrees/<work-item-name>/`. Preserve its existing repository
handling when adapting the reference factory. Do not replace it blindly with a
script whose required packet contract differs from the planner output.

## Target operating model

One Astra medium manager owns the admitted project and handles escalations.
Luna max workers plan, implement, review, and validate, with two shared worker
slots initially. Deterministic scripts prepare workspaces, wait for CI, classify
cycles, and reconcile interrupted progress. Single-project ownership persists
while work waits, validates, or is blocked.

The first delivery slice merges the stabilized, pinned refactor baseline with
current main through a Luna implementation worker and independent Luna review.
The parent migration stays open until its immutable acceptance criteria pass.

Before transferring ownership, verify the admission and recovery mechanisms,
packet contracts, failure routes, and a bounded real dispatch. Record the exact
server endpoint and session; do not reuse another factory's default endpoint.
No new factory has been launched as part of this design checkpoint.

## Repository context

This repository is a Go multi-module agent harness. Use root `AGENTS.md`,
`go.work`, `docs/architecture/architecture-policy.json`, the migration plans, and
module-specific instructions. Do not use the reference factory's own Go/React
product layout or test commands as this project's architecture.

For batch syntax, use `you docs batch-inputs`. Always specify the intended server
and session for remote inspection or submission. Runtime Work and Factory Events
are authoritative; progress documents explain decisions and evidence but do not
replace canonical scheduling state.
