# Single-project operating policy

This factory owns only the admitted audio-runtime project. Read the immutable
manifest, request, acceptance and source plan through FACTORY_PROJECT_MANIFEST
and FACTORY_ROOT. Verify them with `python3 "$FACTORY_ROOT/factory/scripts/project-control.py" status`.
The runtime Work board is the scheduler; progress documents explain decisions.
Never create a second project, weaken acceptance, mark failures passing, or treat
baseline integration as whole-project completion.

Project leadership, planning and meta planning use Astra medium. Execution,
independent review and validation use Luna max. A shared manager slot serializes
planning decisions. Two worker slots bound implementation/review/validation;
review/merge and validation also each have an exclusive slot. Routine meta
planning runs every four hours; project cycles and exceptions have their own
routes. Do not emit extra thoughts loopbacks to defeat the four-hour cadence.

Use only the server in FACTORY_SERVER_URL and the dispatched session ID. Never
use the CLI's default server or restart unrelated factories. Classify failures:
transient, implementation defect, plan defect, missing prerequisite, contract
conflict, or infrastructure. A retry needs a concrete correction or evidence the
blocker cleared; unchanged failures require escalation, not empty CONTINUE loops.
Escalations include criterion, evidence, attempted correction, impact and smallest
operator decision. Blocked ownership persists across restart and elapsed time.

Each slice has one package/shared-surface owner, explicit dependencies, exact
behavioral proof, and named later gates for remaining project criteria. Commit
bounded checkpoints. Preserve other branches, worktrees and user changes. Never
increase architecture baselines or weaken assertions. Use the repository's actual
Makefile/modules; do not copy the reference factory's Go/React test commands.

Implementation delivers a pushed PR with CI started. The scripted CI stage waits.
An independent reviewer verifies behavior, current-head CI and merge readiness,
then merges serially. Green checks on an older head are not current evidence.
No force push to main; no branch deletion or cleanup of unrelated worktrees.
CI/run evidence belongs in local progress and PR description/authorized review
records, not repeated commits that change the head being verified.

Customer and engineering validation are separate fresh missions against the same
immutable artifact. Either FAIL/BLOCKED keeps project acceptance open. Validators
report evidence and never repair the implementation. Paid Realtime probes use
only the existing authorized credential file, never print secrets, and stay within
manifest mission budgets. Physical input needs an intentional operator utterance;
software replay is not proof of acoustic output.
