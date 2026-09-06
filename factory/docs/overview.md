# Go agent harness factory

The factory restores the delivery shape from before the project-cycle expansion:

```text
Astra medium meta-planner -> idea -> Astra medium planner -> isolated workspace
  -> Luna max implementation -> Luna max independent CI/review/merge -> delivery
  -> meta-planner -> fresh Luna max runtime probe -> meta-planner assessment
```

The meta-planner owns reconciliation and project completion. It wakes after
vertical delivery, failure or a probe result, and routinely every four hours.
There is no 15-minute reconciliation cron or separate project leader. Test success
must be followed by exercising the built program. Failed probes become bounded
repairs; final acceptance still requires fresh customer and engineering validation
of all immutable criteria. See operating-policy.md and probe-contract.md.

One project remains admitted. The graph has the original nine workstations plus
two probe preparation/execution stations. A serial manager, two worker slots,
serial merge and serial validation keep ownership bounded. Model profiles remain
Astra medium for planning and Luna max for execution, review and probes. All
agent-worker timeouts are four hours; probe missions retain their own bounded
execution budgets.

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
