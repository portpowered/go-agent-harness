# Meta-planner handoff: fresh simplified board

The user authorized discarding the old board history, restoring the original
factory shape, and making YOU (the Astra medium meta-planner) responsible for
reconciliation, independent runtime probes after verticals, iteration and final
project completion. This is a fresh board, NOT a fresh codebase. Preserve all work.
The prior runtime was stopped deliberately for this change; its interrupted C05
execution is not a new customer failure. Do not revive the old project-cycle graph.

## Immutable objective and baseline

Read factory/projects/audio-runtime/{manifest.json,request.md,acceptance.md,source-plan.md}.
Project audio-runtime; contract audio-runtime-v1. All nine gates remain open:
AUDIO, DEVICE, EMBED, SERVICE, TRACE, REPLAY, FAILURES, QUALITY, PARITY.
The original refactor baseline is 3194edd97aed588f7cdf2f8c58a69ac21da4c9ad.
The original integration bootstrap pin is 8bdafc7f947a3a2c9856220abdc539437035bd21.
Do not change this pin to the new launcher HEAD or duplicate its already merged
history in candidate branches. Baseline merge is only the first vertical.
Factory-only dashboard/recovery/simplification fixes after that bootstrap pin are
on the owning codex/embeddable-agent-runtime branch. Preserve this live factory
configuration and include those follow-up commits deliberately in later delivery;
do not overwrite the operator's simplified graph with an older candidate copy.

## Preserved code and delivery attempts

- PR #396: https://github.com/portpowered/go-agent-harness/pull/396.
  C01 candidate 7225708002c381164733a90a658a9f1f6d326d65 failed required static,
  integration and coverage checks. No independent merged acceptance established.
- C02/C03 each timed out three times at one hour. Their worktrees and checkpoints
  remain. Reuse demonstrated repairs; do not copy uncertain/debug changes wholesale.
- PR #397: https://github.com/portpowered/go-agent-harness/pull/397.
  C04 head 1e324de57aef74a68c4e2c6eec5f5667c6a4f710, verified OPEN and unmerged at
  handoff. Run 34050258936 failed integration, hermetic and coverage; static, unit,
  race, WebMCP Chrome and macOS audio release passed. Recheck live state before edits.
- Current worktree: $FACTORY_ROOT/.claude/worktrees/audio-runtime-c05-baseline-behavior-recovery.
  Branch codex/audio-runtime-c05-baseline-behavior-recovery. HEAD ebb7d5ab at stop
  (merge committed C04 baseline candidate); earlier d7f77077 integrates bootstrap.
  Read its prd.json, progress.txt and any failure matrix before doing more work.
  It has uncommitted modifications to agent-cli/internal/transport/cli/internal/livehost/run.go,
  agent-cli/internal/transport/cli/session_command_support.go,
  agent-cli/internal/transport/cli/session_observability.go,
  go-agent-runtime/services/session/internal/live/session_adapter.go, plus untracked
  go-agent-runtime/services/session/internal/live/session_adapter_test.go.
  Preserve and review these changes. They are hypotheses, not accepted fixes.

## What is failing and what should happen next

C05 targets shipped-session composition, recording/replay and tool continuation:
missing filesystem-tool side effects, unexpected repeated session.update events,
missing provider.json during barge-in finalization, and related integration/
hermetic failures. Start from exact failing tests/logs rather than repairing all
nine migration gates in one task. The existing C05 plan is broad: split work into
one observable behavior at a time, with bounded failing-before/passing-after proof
and checkpoint commits. Preserve worktree ownership and avoid parallel overlap.
You may adopt the existing C05 worktree/unchanged PRD through a same-name idea if
that is the safest recovery; otherwise make a bounded successor preserving it.
Do not rewrite an existing PRD in place behind an active worker.

Neither PR #396 nor #397 is known merged. No final customer/engineering acceptance
exists. The first next action is an evidence review of C05 and fresh GitHub state,
then one bounded recovery slice. After the baseline vertical actually merges,
build an immutable executable and send a fresh independent vertical probe to
confirm launch, public tool effects, continuation/replay and clean shutdown as
appropriate. A green unit test suite alone cannot accept that vertical.

The old 15-minute reconciler ran 35 times and made zero changes. There is no need
to reconstruct those checks or their tokens. Its 56 visible board items mostly
represented history. Old recordings and the pre-simplification board snapshot
remain under the shared Git directory factory-runs for optional forensic reading.
The previous session was b2505f9a-46e3-4571-807c-68fe97883f3d.

Track accepted verticals, artifact hashes, independent probe reports, remaining
criteria and the next action in docs/temp/projects/audio-runtime/meta-status.md.
Use canonical fresh-board Work for dispatch ownership; never claim completion
from this handoff or from a worker's summary alone.
