# Go agent harness factory

This is a single-project delivery factory for the audio/runtime migration.
The admitted project remains open until all criteria in
[its immutable manifest](../projects/audio-runtime/manifest.json) pass. Integrating
the baseline into main is the first delivery slice, not project completion.

## Graph and models

```text
project admission -> Astra medium project lead -> bounded idea + dependent cycle
idea admission -> Astra medium planner -> isolated workspace
 task admission -> Luna max executor -> scripted CI wait -> Luna max reviewer
                                  ^                         | rejected
                                  +-------------------------+
 reviewed merge -> consume slice -> project lead's next cycle
 immutable build -> fresh customer + engineering Luna max validation
                 -> deterministic completion evidence check
```

Astra medium owns project leadership, slice planning and meta planning. Luna max
owns implementation, independent review and validation. One manager slot, two
shared worker slots, one merge slot and one validation slot bound concurrency.
Routine meta planning uses `0 */4 * * *` (every four hours on the factory clock).
Immediate exception routes and project cycles continue independently. A script
reconciles stranded project leadership every fifteen minutes.

The admission record and process lock live in the repository's Git common
directory, shared by every checkout. Waiting, blocking, elapsed time and process
restart do not release ownership. All project/idea/task execution checks the
admitted project and exact contract revision. Completion checks two distinct
canonical validation Work records and every immutable criterion against one
artifact hash. It does not automatically release admission.

## Operation

Build the pinned reference runtime once (source revision
`a82f2e5a532a25b3e163014b28e72190ac28c354`):

```sh
python3 factory/scripts/build-runtime.py --source /path/to/you-agent-factory
```

This installs privately under the Git common directory and records its executable
hash, plus any checked-in compatibility patch hashes. Build input comes from
`git archive`, excluding untracked and ignored source files. The older global
`you` installation failed recording recovery in the launch
probe; the launcher requires this verified build and prepends it to worker PATH.
The builder runs `make build-all` so the production dashboard is embedded in the
CLI. A plain `go build` embeds only the fallback shell on a clean checkout.
Installation requires both the generated dashboard entry page and embed source.

The initial host also had Codex CLI 0.145.0 on PATH, which the provider rejected
for Astra. The factory now resolves a private `factory-bin/codex` wrapper to
`/Applications/ChatGPT.app/Contents/Resources/codex` (0.153.4). Both Astra medium
and Luna max completed a read-only tool-use probe. Keep the executable's companion
resources available: directly symlinking the binary into a new directory caused
the code-mode host lookup to fail; the wrapper executes its original absolute path.
This is local runtime configuration, not a global CLI replacement.

Run from the checkout that owns this factory:

```sh
python3 factory/scripts/run-factory.py status
python3 factory/scripts/run-factory.py start
python3 factory/scripts/run-factory.py stop
python3 factory/scripts/run-factory.py restart
```

The owned endpoint is `http://127.0.0.1:7439`; the launcher records the actual
session identity, process start identity, integration commit and recording path
in `factory-runtime.json` in the Git common directory. `factory-supervisor.log`
and `factory-runs/` are alongside it with private permissions. The checked-in
factory and source checkout are required; this is not a standalone portable bundle.

A stop retains admission and recording evidence. Startup never substitutes a new
project for missing/corrupt recovery evidence. Changed graphs require explicit
recorded-work migration; they are not silently applied to an old session. Check
status after any ambiguous result instead of invoking a second unmanaged runtime.
Do not use `you server` for this factory: the owned recorded run is its launcher.

Use explicit server and session for public CLI inspection. `work list --all`
exposes superseded historical rows: default listing can hide a blocked project
behind its same-name escalation Work. Inspect successor fields and exact Work
identities before deciding that work is missing. Progress documents explain
canonical Work; they do not replace it.

## Verification and remaining migration

`make test-factory-scripts` covers deterministic CI/admission/preflight behavior.
The installed-runtime smoke uses mocked provider output and exercises delivery,
review rejection, escalation and recording resume without paid model calls. Configuration validation
alone does not prove delivery or recording recovery.

Read [operating policy](operating-policy.md), [handoff plan](handoff-plan.md), and
[baseline status](baseline-status.md) for source identities, architecture ownership,
first integration requirements and remaining audio/runtime acceptance. Never
replace the harness's Go workspace gates with the reference factory's test suite.
