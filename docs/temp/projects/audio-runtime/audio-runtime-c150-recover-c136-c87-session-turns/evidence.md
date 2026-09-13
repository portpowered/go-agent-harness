# C150 recovery evidence

Project: `audio-runtime`  
Contract revision: `audio-runtime-v1`  
Task: `audio-runtime-c150-recover-c136-c87-session-turns` (`work-task-35`)  
Admitted branch: `codex/audio-runtime-c150-recover-c136-c87-session-turns`

## Pre-integration preservation checkpoint — 2026-09-13

Admission was verified with `factory/scripts/project-control.py verify-work
--type task --name audio-runtime-c150-recover-c136-c87-session-turns`; the
result was `admitted` for the sole `audio-runtime` project. `prd.json` has the
same branch name as this isolated C150 worktree. The C150 worktree was clean at
`4a1c399ccbb3d780be95eb04316e84b8f11a6646`, which is the freshly fetched
`origin/main` and includes the required startup ancestor
`8bdafc7f947a3a2c9856220abdc539437035bd21`.

The implementation is adopted from the existing C136 checkout, not rebuilt:

- C136 worktree: `/Users/abdifamily/.codex/worktrees/af44/go-agent-harness/.claude/worktrees/audio-runtime-c136-recover-c87-session-turns`
- C136 branch: `codex/audio-runtime-c136-recover-c87-session-turns`
- C136 checkpoint: `7268f46a8c23cea480aecba621d49c01e4a30aac`
- PR: `#513`, whose head was `7268f46a8c23cea480aecba621d49c01e4a30aac`
- C136 dirty path: `docs/temp/projects/audio-runtime/audio-runtime-c136-recover-c87-session-turns/evidence.md`
- Dirty working-tree file SHA-256: `e10d62497e8b8a293574e482c754e4f863800c57c97a991da360f2a47b0f7a77`
- Committed C136 blob SHA-256 before the append: `1d47e4374a31827dbf3a2e0c855dc826ca9d9219b2d06e2266b49cdee2c0ec99`
- Dirty delta: exactly 48 added lines, zero deletions

The C136 checkout and branch were not reset, rebased, cleaned, or otherwise
mutated. This C150 record checkpoints the dirty append before integration; the
original C136 worktree remains the authoritative preserved source until the
same append is carried forward into the adopted candidate.

## Preserved latest rejection and ownership

PR #513 run `34772088096` evaluated head `7268f46a8c23cea480aecba621d49c01e4a30aac`.
The static job failed only on the 12 stale downward session-turns baseline
entries and the unregistered generated file
`go-agent-runtime/services/sessionturns/wire/wire_gen.go`; vet, lint and
staticcheck succeeded. The coverage job independently failed only on
`TestManagedBrowserManagerReusesStateAndClosesOnlyOnExplicitClose`, where
TempDir cleanup found a non-empty WebMCP browser profile. That package is out
of C150 scope; focused rechecks passed 10/10 normal and 5/5 coverage trials.
This external non-reproduction is preserved as unresolved evidence, not called
fixed and not used to authorize WebMCP edits.

C110 PR #497, C111 PR #496, and C119 PR #505 remain open and retain the exact
shared registry/policy leases. C150 must not edit their paths until their
guarded merges release them. No prior independent review findings exist for
PR #513 (`reviews: []`); the latest CI rejection is the authoritative repair
inbox. No CI-green, review, merge, vertical-probe, or project-acceptance claim
is made by this checkpoint.

Next action: integrate the preserved C136 implementation into this C150
worktree without losing current-main or peer ancestry, then wait for or verify
release of the exact shared leases before applying only the demonstrated
session-turns registry and downward baseline repairs.

## Adopted candidate focused recheck — 2026-09-13

The C150 branch preserves the exact C136 checkpoint through merge commit
`7fee7f7826a16a898557d95c171b79ce4f18feef` and the verifier-only checkpoint
`8a3a5b9`. The C136 evidence file's carried-forward append has SHA-256
`e10d62497e8b8a293574e482c754e4f863800c57c97a991da360f2a47b0f7a77`, equal
to the untouched C136 worktree file. Required startup, C136, and fresh-main
ancestry checks pass; `git diff --check` is clean and the C136 checkout remains
dirty only in its original evidence file.

Focused evidence on the adopted candidate:

- sessionturns normal and race tests: 48 passed in three packages each;
- deprecated CLI compatibility: 6 normal and 2 race tests passed;
- GOWORK=off external consumer: passed;
- inherited mutation, retirement/adapter, and owned/excluded-path verifiers:
  passed after allowing only the C150 evidence namespace;
- bounded credential-free audio/tool and interruption/tool runner: both cases
  passed with bounded output, reaped children, and zero survivors;
- accumulated session regressions: normal, coverage, and race passed, including
  all 20 high-rate trials and expected negative controls;
- targeted vet, pinned staticcheck 2026.1, pinned golangci-lint 2.9.0, format,
  Wire regeneration/check, coverage registration (195 packages across six
  modules), and diff checks: passed.

The bounded architecture check still reports exactly the known 13 findings:
the twelve stale session-turns entries and the unregistered
`go-agent-runtime/services/sessionturns/wire/wire_gen.go`. C110 PR #497, C111
PR #496, and C119 PR #505 are still open, so C150 has not edited either shared
registry/policy path. The WebMCP TempDir failure remains out-of-lease and
non-reproduced (10/10 normal and 5/5 coverage focused repetitions); it is not
called fixed. No CI-green, review, merge, vertical, or project-acceptance claim
is made.

Next action: recheck the exact C110/C111/C119 guarded-merge lease state; once
released, fetch current main, apply only the demonstrated sessionturns Wire
registration and twelve downward baseline deletions while preserving peers,
rerun the focused and accumulated gates, then push the changed head to PR #513
through SCRIPT CI without polling.

## Changed behavioral coverage and lease recheck — 2026-09-13

`make coverage-changed COVERAGE_BASE=4a1c399ccbb3d780be95eb04316e84b8f11a6646`
exited 0. The complete module coverage profiles, embedded runtime coverage,
and coverage gate passed; the worktree remains clean.

Admission was reverified as `admitted` for the sole `audio-runtime` project.
C110 PR #497 remains open at `a1dab09a990a96f08fc21fc2977b8f7dad4e5a98`,
C111 PR #496 remains open at
`d014a3586368c37e20618481ee162e1b83db113f`, and C119 PR #505 remains open at
`a15b8112c84b7f3a6eb98c176094237b29d97e11`; their exact shared leases remain
held. PR #513 remains open/draft at its preserved C136 head
`7268f46a8c23cea480aecba621d49c01e4a30aac`, so no knowingly red candidate was
pushed and no second PR was opened.

Exact next action: retain C150 ownership until those guarded merges release
the leases, then fetch current main and apply only the one sessionturns Wire
registration plus the twelve demonstrated downward baseline deletions. After
the focused and accumulated gates pass, push the changed C150 candidate to
the existing PR #513 and return `ACCEPTED` to Script CI without polling.

## Bounded validation while shared leases remain held — 2026-09-13

At candidate `07d3388fd7658fca6887ca66c514716f7610921`, the focused causal
checks were rerun without source or shared-file changes. Sessionturns normal
and race each passed `48` tests; deprecated CLI compatibility passed `6`
normal and `2` race tests; the `GOWORK=off` external consumer passed; and all
three inherited verifier modes passed. The bounded credential-free audio/tool
and interruption/tool public cases each passed `3` tests with bounded output,
reaped children, and no survivors. The accumulated session regression matrix
passed normal, coverage, and race modes, including all `20` high-rate trials
and its expected negative controls.

The worktree remains clean. The shared leases are not released: PR `#497` is
open at `a1dab09a` with its Windows portable lane failed, PR `#496` is open at
`d014a358` with its coverage lane failed, and PR `#505` is open at
`00f53923` with a new CI run still in progress. No guarded merge or release
was observed, so C150 did not edit `scripts/wire-packages.txt` or
`docs/architecture/architecture-policy.json`, and did not resubmit the
unchanged 13-finding architecture rejection. The WebMCP TempDir cleanup
failure remains the previously recorded out-of-lease, non-reproduced result.

Next action is unchanged: after C110/C111/C119 guarded merges release the
exact paths, fetch the then-current `origin/main`, integrate it while
preserving C136, apply only the sessionturns Wire registration and twelve
demonstrated downward baseline deletions, rerun bounded gates, commit/push
the same PR `#513`, and return `ACCEPTED` to Script CI without polling.

## Fresh bounded causal recheck and lease hold — 2026-09-13T21:20:08Z

Admission was reverified with
`factory/scripts/project-control.py verify-work --type task --name
audio-runtime-c150-recover-c136-c87-session-turns --root "$FACTORY_ROOT"`;
the sole `audio-runtime/audio-runtime-v1` project remains admitted. The
isolated branch still matches `prd.json.branchName`, the worktree is clean at
`0af1423458f4a33967c696083caae5a7a8c01b4d`, and startup
`8bdafc7f947a3a2c9856220abdc539437035bd21`, inherited C136
`7268f46a8c23cea480aecba621d49c01e4a30aac`, fresh main
`4a1c399ccbb3d780be95eb04316e84b8f11a6646`, and `origin/main` all remain
ancestors. `git diff --check` is clean.

Fresh focused evidence at this exact head passes: sessionturns normal/race
`48/48` tests, CLI compatibility normal/race `6/2`, the separate
`GOWORK=off` consumer, all three inherited verifiers, both credential-free
public cases (`3/3` each, bounded output, reaped children and no survivors),
and `COUNT=1 bash scripts/test-session-ci-regressions.sh all` in normal,
coverage and race modes. The expected replay/audio/transcript negative
controls and all 20 high-rate trials remain green.

`make architecture-size-check` still fails closed with exactly the previously
demonstrated 13 findings: twelve stale downward `session_turns.go`/test
entries (`ErrEmptyTurn`, `ErrInvalidTurnDirection`, `ErrInvalidTurnTick`,
`ErrMissingTurnInferencer`, `ErrSessionClosed`,
`ErrSessionEndedWithActiveTurn`, `ErrTurnAlreadyActive`,
`ErrTurnEndWithoutStart`, `ErrTurnMismatch`, `readTurnResponse`,
`TestSessionTurns_FiveTurnsUseOnePersistentSessionAndExactLifecycle`, and
`noTurnSetup`) plus the unregistered generated
`go-agent-runtime/services/sessionturns/wire/wire_gen.go`. No shared path was
edited.

Canonical `work-task-35` remains `init/PROCESSING`. PR #497 is open with its
Windows portable lane failed, PR #496 is open with its coverage lane failed,
and PR #505 is open with integration, coverage and hermetic lanes still
pending; none has a guarded merge releasing the shared registry/policy paths.
PR #513 remains open/draft at the inherited C136 head with no independent
review findings. The external WebMCP TempDir failure remains preserved,
out-of-lease and non-reproduced; C150 made no WebMCP change.

Exact next action: retain C150 ownership until C110/C111/C119 guarded merges
release the exact shared paths, then fetch and integrate the accepted current
main, apply only the sessionturns generated-Wire registration and twelve
demonstrated downward baseline deletions while preserving peers, rerun bounded
gates, commit/push the changed same PR #513 head, and return `ACCEPTED` to
SCRIPT CI without polling.

## Exact lease and PR recheck — 2026-09-13T21:25:51Z

Admission was reverified with `project-control.py verify-work --type task
--name audio-runtime-c150-recover-c136-c87-session-turns --root
"$FACTORY_ROOT"`, returning `admitted`. The canonical task remains
`work-task-35` in `init/PROCESSING`; no review row exists. C150 is clean at
`3d9e47400dbd0613c9b8d96b1ce5c605e6ef6a16`, with startup, inherited C136
`7268f46a8c23cea480aecba621d49c01e4a30aac`, fresh accepted main
`4a1c399ccbb3d780be95eb04316e84b8f11a6646`, and `origin/main` ancestry
verified. C136 remains cleanly preserved at its original checkpoint with only
the original 48-line evidence append dirty.

The exact shared-path release is still unavailable. PR #497 (C110) is open at
`a1dab09a990a96f08fc21fc2977b8f7dad4e5a98` with eight green checks and a
Windows portable failure; PR #496 (C111) is open at
`d014a3586368c37e20618481ee162e1b83db113f` with eight green checks and a
coverage failure; PR #505 (C119) is open at
`00f53923d61b32a27cf4d6ebc5bbf0b4c1ae6e3e` with all nine checks green but no
independent review or guarded merge. None releases `scripts/wire-packages.txt`
or `docs/architecture/architecture-policy.json`, so C150 made no shared-file
mutation and did not resubmit the unchanged 13-finding candidate. The external
WebMCP TempDir cleanup failure remains the recorded out-of-scope,
non-reproduced result.

Next action remains: after the three exact leases are released by guarded
merges, fetch the then-current main, integrate it without rewriting C136/C87,
apply only the sessionturns generated-Wire registration and twelve downward
baseline deletions, rerun the bounded focused and accumulated gates, then push
the changed same PR line and return `ACCEPTED` to SCRIPT CI without polling.

## Fresh owned recheck while shared leases remain held — 2026-09-13

At pre-checkpoint head `4c191e05217bccbee6f0497b797cb7b13ca3405f`, the owned
causal recheck passed: sessionturns normal and race suites, deprecated CLI
compatibility normal and race suites, the separate `GOWORK=off` consumer, all
three inherited C136 verifiers, both credential-free public cases, and
`COUNT=1 bash scripts/test-session-ci-regressions.sh all` in normal, coverage
and race modes. The accumulated matrix passed all 20 high-rate trials and
retained the expected replay/audio/transcript negative controls. The Wire
regeneration/check completed successfully and `git diff --check` remained
clean.

`make architecture-size-check` failed closed with exactly the known thirteen
deferred findings: twelve stale downward entries for the retired C87
`session_turns.go`/test symbols and the unregistered generated
`go-agent-runtime/services/sessionturns/wire/wire_gen.go`. No source, peer,
shared registry, policy or baseline path was changed.

The live PR snapshot still shows C110 PR #497 open with its Windows portable
lane failed, C111 PR #496 open with its coverage lane failed, and C119 PR #505
open with all nine checks green but no guarded merge. Therefore the exact
shared leases remain held. PR #513 remains open/draft at inherited C136 head
`7268f46a8c23cea480aecba621d49c01e4a30aac` with no independent review. The
external WebMCP TempDir failure remains preserved as out-of-lease,
non-reproduced evidence. No CI, review, merge, probe or acceptance claim is
made.

Exact next action: retain `work-task-35` until C110/C111/C119 guarded merges
release their exact paths; then fetch the accepted current main, integrate it
without rewriting C136/C87, apply only the sessionturns generated-Wire
registration and twelve demonstrated downward baseline deletions, rerun the
bounded gates, push the changed same PR #513 head, and return `ACCEPTED` to
SCRIPT CI without polling.
