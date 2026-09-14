# Audio runtime convergence handoff — 2026-09-13

This is the authoritative fresh-board context for the single admitted
`audio-runtime` project. The operator has authorized broad technical decisions,
eight shared agent slots, Sol medium planning, Luna xhigh execution/review and
four-hour worker turns. Preserve useful branches, worktrees, commits and open PRs,
but rebuild canonical Work from the integrated repository and current remote state.

## Outcome and progress measure

The goal is a converged, embeddable runtime. Business logic belongs behind thin
service contracts with implementation under `services/<vertical>/internal` and
service-local Wire composition. The CLI is a presentation/transport adapter. The
agent loop exchanges audio through buffers and never owns a device. Audio packet
parsing, clocks, DSP and trace/replay operations have one subsystem; device access
has one gateway. The legacy
`agent-cli/internal/services/internal/agentruntime` package must reach zero
production files, except an explicitly approved stateless compatibility adapter
outside that directory. Any required CLI compatibility surface is limited to at
most two production files and 300 physical lines for each retired vertical.

At `origin/main` revision `1b1c0296b9471b930c2b290f3bf6fa10559957de`, the
legacy directory contains 106 production Go files and 39,329 physical lines
(excluding `_test.go`). Recalculate this inventory on every meta-planner wake.
The primary health signal is integrated deletion from that directory. Busy workers,
new subservices, aliases, documentation and proof artifacts do not count.

Each admitted implementation owns a complete coherent vertical. Its packet names
all callers, the destination service contract/internal implementation, and the
exact legacy files and line count that will be deleted. Carry it through caller
cutover, deletion, full functional CI, independent review and guarded merge. A
partial checkpoint remains CONTINUE. If active work has no concrete deletion
target, or the integrated total does not materially fall, treat that as a planning
failure and immediately change the task mix, ownership or instructions.

## Recovery inventory

The stopped board had useful work named C155, C167, C173, C174, C176 and C177
covering virtual playback low-watermark behavior, RTC cancellation, session turns,
interruption replacement replay, provider close handling and public trace CI.
Inspect their worktrees, branches, PRs and checkpoints before admitting replacements.
Adopt useful code into complete retirement verticals; do not recreate stale Work IDs
or accept these narrow repairs as vertical completion. Resolve exclusive path
ownership before dispatch. Prefer the largest pairwise-disjoint legacy clusters and
keep all eight shared slots occupied when eight useful independent verticals exist.

## Acceptance and reporting

Keep only tests that prove public behavior, important public failures or bounded
shutdown. Delivery requires the repository full functional CI on the candidate
head, independent review and guarded merge. Do not emit model validation Work,
stage probe artifacts, require immutable proof packets, or add per-slice evidence
machinery. Final project completion additionally requires the meta-planner to
inspect the integrated source boundaries and run the architecture suite against
the original customer outcomes. If the code is still duplicated or the legacy
directory remains, continue planning and delivery.

Operator reports have exactly four short lines: active worker count; active
verticals; current legacy production files/lines plus the exact files/lines targeted
for deletion by current workers; and what the operator must provide, or `none`.
