# Single-project operating policy

The primary Astra medium meta-planner owns the admitted audio-runtime project,
reconciliation and acceptance. Astra medium plans slices. Luna max executes,
reviews independently and probes delivered artifacts. Agent-worker dispatches have
a four-hour timeout; individual test/probe commands keep bounded deadlines. Preserve one active project
and at most one implementation vertical; shared manager capacity is one. Routine
meta planning runs every four hours. Delivery/probe completion and failure wake
it immediately. Successful routine inspection is not a reason to emit more Work.

The delivery graph is the original ideafy / plan / setup-workspace / process /
review / consume flow with loop breakers, plus prepare-validation / validate.
There is no project-lead, project-cycle or periodic reconciliation workstation.
The existing project Work is an acceptance marker controlled by the meta-planner,
not a second scheduler. The durable admission/owned launcher prevent another
project from starting. Keep the current model profiles and private full UI build.

Read meta-planner-handoff.md before choosing the first task. Current code, PRs,
branches and dirty work survive the fresh board. Preserve them; do not restart the
entire refactor or treat earlier failed work as discarded. Record exact source
commits, task ownership and residual failures. Never reset a user's checkout.
Frequent bounded commits allow recovery without repeating a whole vertical.

Slice success requires a reviewed merge with required current-head CI passing.
The independent reviewer runs ci-wait.py; there is no separate CI workstation.
After delivery the meta-planner sends an independent scope=vertical probe against
the exact built executable. The probe must launch and exercise the shipped public
workflow, check observable behavior and shutdown, and recheck an existing workflow.
Tests, type checks and mocks alone never establish runtime acceptance. A failure
produces a small demonstrated repair and a new probe; preserve negative controls,
assertions, timeouts, architecture limits and explicit unresolved evidence.

Final completion requires all nine immutable criteria and two fresh independent
scope=project reports (customer and engineering) on one artifact. The meta-planner
must read both reports, confirm canonical validation Work completion and pass
project-control.py verify-completion before moving project Work to complete.
A vertical PASS never substitutes for project completion. Missing prerequisites
or physical device evidence remain BLOCKED; software replay is not acoustic proof.

Use the immutable manifest under factory/projects/audio-runtime. No agent may
weaken acceptance, admit another project or enlarge the authorized Realtime
budget. Never print credentials. New tests should prove behavior or failure
handling; do not write implementation-mirroring assertions just to raise counts.
No automatic unchanged-failure retries by the meta-planner. Investigate, narrow,
checkpoint, or state the precise external blocker. Preserve admission when blocked.
