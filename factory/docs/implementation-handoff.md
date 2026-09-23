# Implementation handoff

This document is the executor handoff policy for admitted Factory work.

## Admission and evidence

Use the admitted project manifest and the canonical Factory board for the
session. Before changing code, verify the task with
`project-control.py verify-work --type task --name <task>`, read the task and
all concluded review feedback, and verify that `prd.json.branchName` matches
the isolated worktree branch. Local progress files and evidence artifacts do
not replace board state. Do not create a second project, acceptance waiver,
probe artifact, mutation script, external-consumer fixture, or moved test to
manufacture completion.

## Executor loop

The executor retains ownership while an actionable implementation or repair
remains. It reproduces review findings, makes the smallest complete repair,
runs the focused causal tests and accumulated regressions, and preserves
predecessor checkpoints and unrelated changes. For baseline integration it
fetches the current `main`, preserves required ancestry, and never resets or
merges the running host checkout.

Before handoff, the executor commits and pushes the candidate and opens or
updates its pull request. `ACCEPTED` means only that the exact pushed head is
ready for the script-owned full functional CI gate; it does not mean CI is
green. The executor must not poll CI, wait for review, or self-review.

## Decisions

Return `CONTINUE` with exact evidence and the next action when implementation
or repair remains, including after a failed test. Return `ACCEPTED` only after
all planned callers are integrated, the owned legacy production cluster is
absent, the adapter cap is verified, focused and accumulated local checks have
run, and the exact candidate has been pushed for script CI. Return `FAILED`
only for unresolved ambiguity, ownership conflict, unavailable prerequisite,
or demonstrated lack of progress that requires replanning; a red test alone
is not escalation.
