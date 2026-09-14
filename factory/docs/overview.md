# Go agent harness factory

The factory restores the delivery shape from before the project-cycle expansion:

```text
Sol medium meta-planner -> idea -> Sol medium planner -> isolated workspace
  -> Luna xhigh implementation -> hermetic functional CI
  -> Luna xhigh independent review/merge -> delivery -> meta-planner reconciliation
```

The meta-planner owns reconciliation, convergence and project completion. It wakes
after delivery or failure and routinely every four hours. Every wake measures the
production files and lines remaining in the legacy agentruntime package, maps active
work to exact deletions and changes the task mix when that total is not falling.
A vertical completes after caller cutover, legacy deletion, full functional CI and
independent review/merge. Final acceptance requires qualitative inspection of the
integrated architecture and the full functional/architecture suites. See
operating-policy.md and meta-planner-handoff.md.

One project remains admitted. Eight shared agent slots cover planning,
implementation and review/merge; CI is a script gate and consumes no agent slot.
Model profiles are Sol medium for planning and Luna xhigh for execution and review.
All agent-worker timeouts are four hours.

The user authorized a fresh board. meta-planner-handoff.md preserves the earlier
PRs, branches, current dirty work, exact baseline and outstanding failures. Old
recordings remain archived in the shared Git directory; they are not resubmitted.

Build the pinned private runtime with production dashboard assets:

```sh
python3 factory/scripts/build-runtime.py --source /path/to/you-agent-factory
python3 factory/scripts/run-factory.py status
python3 factory/scripts/run-factory.py start
python3 factory/scripts/run-factory.py restart
```

The builder runs make build-all from a clean Git archive with checked-in recovery
patches. Plain go build embeds only a fallback dashboard. The owned endpoint is
http://127.0.0.1:7439/dashboard/ui. Normal restarts resume the saved board. The
explicit fresh-board operation requires a committed context file, archives the
prior runtime and preserves project admission; it is not a routine retry.
Private runtime and Codex wrappers live in the repository's shared Git directory.
The Codex wrapper uses the app CLI at its original resource path (companion files
must remain accessible). This leaves global CLI installations unchanged.
