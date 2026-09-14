# Single-project operating policy

The primary Sol medium meta-planner owns the admitted audio-runtime project,
reconciliation and acceptance. Sol medium plans verticals. Luna xhigh executes and
reviews independently. Agent-worker dispatches have
a four-hour timeout; individual test commands keep bounded deadlines. Preserve one active project
and an independent-work ceiling of at most 10; shared manager capacity is one. Routine
meta planning runs every four hours. Delivery completion and failure wake
it immediately. Every wake should advance useful work and fill available capacity from remaining
project gaps. Routine inspection must not become an excuse for leaving ready work idle.

Running workers are not a progress metric. On every meta-planner wake, measure the
integrated-main `agent-cli/internal/services/internal/agentruntime` production Go
files (excluding `_test.go`) and their physical lines, compare with the prior wake,
and map every current worker to the exact legacy files and lines its vertical is
expected to delete. If material net legacy retirement is stalled, diagnose the plan
failure and change the task mix, ownership or instructions in that wake. Optimize
toward the converged service-owned structure rather than reporting health from
worker activity.

Operator updates are concise and contain only the worker count, active verticals,
current legacy runtime production file/line size, exact legacy files/lines current
workers are expected to remove, and any specific user attention needed (`none` when
there is none).

The delivery graph is the ideafy / plan / setup-workspace / process / ci-gate /
review / consume flow. It has no model-driven validation route.
There is no project-lead, project-cycle or periodic reconciliation workstation.
The existing project Work is an acceptance marker controlled by the meta-planner,
not a second scheduler. The durable admission/owned launcher prevent another
project from starting. Keep the current model profiles and private full UI build.

Read meta-planner-handoff.md before choosing the first task. Current code, PRs,
branches and dirty work survive the fresh board. Preserve them; do not restart the
entire refactor or treat earlier failed work as discarded. Record exact source
commits, task ownership and residual failures. Never reset a user's checkout.
Frequent bounded commits allow recovery without repeating a whole vertical, but a
checkpoint is not a completed unit of progress. The completion unit is one whole
vertical retirement carried through integration: service ownership, caller cutover,
legacy deletion, full functional CI, independent review and merge.

Vertical success requires a reviewed merge with required current-head CI passing.
Before the first PR is opened, and before every later push after a CI or review
repair, the executor must run `make prepush` from the repository root plus every
vertical-specific functional, integration, race, hermetic, and generated-Wire
command named by the plan. All must pass on the exact commit being pushed. A failing
local gate remains with the executor and must not enter script CI. Only a check that
cannot run locally because it requires a hosted operating system or service may be
deferred, and the executor must name that check and why. Opening a draft PR does not
waive this rule. The PR description or executor handoff records the exact commands,
commit SHA, and pass results.
The script CI workstation invokes ci-gate.py, which delegates polling to ci-wait.py, before review. CI failures return to
the same executor; bounded infrastructure exhaustion wakes meta. Review does not poll.
Required CI is the repository's full functional CI on the current candidate head;
it confirms the executor's local preflight rather than discovering ordinary lint,
compile, unit, architecture, race, or hermetic failures for the first time.
Keep focused tests only when they prove public behavior, an important public failure
or bounded shutdown; do not add per-slice proof scaffolding. Once full functional CI
is green, independent review may merge and the meta-planner moves forward immediately.
Do not create validation Work, stage
artifacts, hash probe inputs, launch a model validator or require a post-merge
vertical report. A later integration failure returns a small demonstrated repair
to the owning task. Preserve useful negative controls, assertions, timeouts,
architecture limits and explicit unresolved evidence in normal tests and CI.

Final completion requires the meta-planner's qualitative inspection against all
nine criteria, the repository's green full functional CI and its architecture
suite on integrated main. Routine vertical reports and model validators are not completion
prerequisites. Missing software functionality remains open; authorized physical-
device exclusions remain explicit and do not block completion.

Use the immutable manifest under factory/projects/audio-runtime. No agent may
weaken acceptance, admit another project or enlarge the authorized Realtime
budget. Never print credentials. New tests should prove behavior or failure
handling; do not write implementation-mirroring assertions just to raise counts.
No automatic unchanged-failure retries by the meta-planner. Investigate, narrow,
checkpoint, or state the precise external blocker. Preserve admission when blocked.

## Stabilization and executor ownership

Admit ready independent Work up to the ten-item backlog ceiling in one validated
batch. The former one-Work-per-invocation restriction is removed by user direction.
Note: "ready" here means dependencies are satisfied, not a JSON state name.
New idea packets must omit `state` (default `init`) or use the authored `init`.
Never use validation's `ready` state for an idea. Before returning a generated
batch, check each work type/state against factory.json and run submit batch
--dry-run for structural validation; dry-run alone does not prove graph-state
validity. An invalid generated state can stop the engine and cancel other workers.

Eight-slot capacity is authorized for the current retirement backlog. Target eight useful concurrent delivery
items and prepare follow-on work before active items finish. Executors keep ownership through CONTINUE
while actionable implementation or required test repair remains; a red check is
not itself an escalation. Escalation requires ambiguity, conflicting ownership,
unavailable prerequisites or demonstrated lack of progress requiring replanning.
These rules supersede earlier task packets that required stopping at a small fix
while known necessary candidate repairs remained. Two-hour operator checks supplement
the four-hour meta cadence while terminal-failure notification is being repaired.

The process and review routes have no visit-count loop breakers. A prior guarded
logical move repeatedly re-fired after the threshold, producing more than 2,000
initial tokens from one C119 task and making the Work and Worker Sessions APIs
unusable. The processor's `REPEATER`/`CONTINUE` route retains the same task and
owner for as many actionable repair turns as needed. Actual worker failure and
review failure still produce one explicit meta-planner wake through their authored
failure routes; ambiguity and ownership conflicts return through those routes rather
than through an automatic visit ceiling.

The ten-item ceiling counts currently runnable or dependency-queued delivery
tasks and reviews. It does not count terminal task/review attempts,
completed work, retained parent `idea:to-complete` tokens, or a historical C-number
range. Those records remain as evidence but must never prevent replacement Work
from filling a free executor slot. Before declaring the ceiling full, list the ten
specific nonterminal delivery identities and their current canonical states. If
fewer than ten exist, admit disjoint follow-on work immediately; if shared capacity
is below eight, fill it unless each vacancy has an exact file/API ownership conflict.

## Throughput and admission

Keep a ready backlog of up to ten independent delivery items. Admission is not an
executor lease: queued work and work waiting on script CI must not reserve an idle
agent slot. The scheduler enforces the deployed eight shared execution/review
slots. Refill ready work in the same wake; do not wait for a staged promotion,
another merge, or the next periodic planning interval. Reviews and
implementations compete in the same pool; only final merge mutations serialize.

Count actual running agents separately from queued items, script jobs and blocked
work. Defer a vertical only for a concrete dependency or conflicting file ownership;
identify its next owner/action immediately. Keep independent work moving during
CI, review and failure diagnosis. Escalate measured resource contention with evidence,
not speculative caution. Ten admitted items is a ceiling, not a requirement to
invent work. Operator may increase live capacity toward ten at a restart checkpoint;
never edit the running definition or bypass its resume hash guard. Read
operator-recovery-status.md for deployed capacity and active ownership.

## Qualitative final review

Before final completion, the meta-planner must inspect the actual integrated code
and public behavior against original user intent and every criterion,
including subsystem cohesion, clocks/DSP/parsing centralization, device isolation,
buffer-based I/O, per-service internals/Wire, embeddability, trace/replay fidelity
and reported failure modes. Record inspected revision/files and per-criterion
judgments in qualitative-acceptance.md. Tests, merged PRs and an empty queue do not
substitute for this review. Any material gap requires further planning, complete
vertical execution, full functional CI and independent review, then another
qualitative assessment. Retain one project
admission; do not create unrelated projects to fill capacity.

## Current delivery ownership

The executor must pass the mandatory local preflight before each PR submission or
repaired push. It then submits to the script CI gate; green CI admits independent
review, while check failures return to the same executor. The script gate owns
hosted CI polling so agents do not duplicate that polling.

The factory is named go-agent-harness; its sole admitted project is audio-runtime.
The ideafy workstation is the meta-planner. thoughts:init wakes it for inspection;
it does not automatically authorize replanning or retries. though-retrigger only
creates the scheduled meta wake; it does not independently reconcile project work.
Review:fin retains the concluded review attempt while the same task returns to
execution or CI. A new review attempt is created after CI passes again.

## Deployed eight-slot shared pool

User authorized the initial stage4 pool on 2026-09-09 and expanded it to eight
on 2026-09-11 after the factory sustained full utilization and accumulated a
concrete disjoint retirement backlog. Execution and review share eight
executor-slot tokens. Review holds no separate
serial resource; only the final merge helper serializes GitHub mutations. Keep
Sol medium planning and Luna xhigh execution/review, all4h limits.
Admit multiple useful independent items per invocation, up to ten admitted items;
prepare follow-on work during review so execution capacity does not sit idle.
A review is not a global prerequisite for unrelated work. Keep paths/dependencies
explicit and the single project. Eight is deployed concurrent agent capacity; ten is the admitted-work ceiling.

## Aggressive execution posture — user direction 2026-09-09

This section supersedes older one-at-a-time admission and conservative waiting
language in saved prompts, handoffs and task packets. Keep the shared pool busy
with concrete project work. Plan and admit multiple disjoint retirement verticals each wake up
to the ten-item admission ceiling; do not wait four hours between admissions. Diagnose failures
while other implementations continue. Resolve ownership during the same decision,
splitting paths or assigning a bounded diagnosis when mutation overlap is real.
Require a specific file/API dependency to defer work; broad service overlap alone
is insufficient. Preserve active checkpoints and give blocked tasks an immediate
next owner/action. Prefer implementation and useful failure diagnosis over more
audit paperwork. Expand toward ten when a concrete independent backlog justifies
it; the scheduler limits concurrently running shared-pool agents to eight. CI, independent review,
truthful evidence, final acceptance and exclusive file ownership remain mandatory.

## User scope amendment — 2026-09-10

The user explicitly excludes native Windows hardware/endpoints and physical acoustic
testing from this migration because it is not currently feasible. Retain Windows
software execution/compilation and hermetic CI validation. Record excluded hardware
proof as OUT OF SCOPE, never as PASS, and do not block software delivery or project
completion solely on that excluded prerequisite. This user direction supersedes
older manifest/mission wording that demanded that hardware evidence. The primary
must reconcile acceptance bookkeeping through the supported contract amendment
path, preserving original reports and the scope-change provenance. Do not silently
reinterpret a historical failed report as a passing execution.

Keep the primary outcome service decomposition: migrate remaining CLI runtime
business logic into go-agent-runtime services/X/internal behind thin contracts and
per-service Wire, then remove redundant CLI implementation. Report concrete remaining
CLI ownership and retired code alongside scoped test/repair counts; those counts
are not a migration percentage. Current operator main926ded7b audit found107 production
Go files /41305 lines (including comments/blanks) in CLI services/internal/agentruntime.

The 2026-09-11 exact baseline/current comparison found that the principal
`agent-cli/internal/services/internal/agentruntime` package remained at 107
production Go files and changed only from 41,297 to 41,129 physical production
lines. Treat substantial retirement of this package as the highest remaining
architecture priority. Sol planning must keep an ordered backlog of several
cohesive, pairwise-disjoint retirement verticals whenever ownership permits.
Rank the largest coherent legacy clusters first and assign one owner to each
cluster through integration. Every plan inventories the complete cluster, names
the exact legacy production files it will delete, names every caller it will cut
over, and sets its deletion target from that full inventory. Completion means all
business decisions and mutable runtime state have moved to
`services/<vertical>/internal`, all callers use the service-owned contract, and
the corresponding `agent-cli/internal/services/internal/agentruntime` production
files are deleted. The deletion target is the whole owned cluster, not an arbitrary
net-line floor or a count selected from the easiest files.

The only CLI remainder for a retired vertical is a stateless transport or required
compatibility adapter outside the retired agentruntime region. Its explicit default
budget is at most two production files and 300 physical production lines total;
the plan must name those files and may set a smaller budget. Any exception requires
a concrete immutable public compatibility constraint and meta-planner approval
before implementation. Aliases, projections or wrappers that retain business
decisions, composition ownership or mutable state violate this budget and leave the
vertical open.

A new service beside retained duplicate legacy logic is not retirement. Neither
aliases, projections, evidence documents, mutation scripts, probe artifacts,
external-consumer fixtures, moved tests nor new service lines count toward deletion
or completion. Do not use an arbitrary net-line floor as a success condition. Carry
the vertical through caller integration and delete the owned legacy region in the
same task; a follow-up deletion task is permitted only for a demonstrated external
dependency and leaves the vertical incomplete. Re-run the integrated inventory
after each retirement and continue until the CLI package contains only budgeted
transport, presentation and compatibility adapters.
