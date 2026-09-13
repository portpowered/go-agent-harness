# C136 recovery implementation checkpoint

At the start of implementation, admission was reverified with
`factory/scripts/project-control.py verify-work --type task --name
audio-runtime-c136-recover-c87-session-turns`, returning admitted for the sole
`audio-runtime/audio-runtime-v1` project. The isolated branch exactly matches
`prd.json.branchName`, the worktree is clean, and refreshed `origin/main`
is `bd6a1289218d1bef1a3af36e64e9d4496062416f`. The preserved C87 remote ref
still equals immutable PR 476/head
`28b5a9b18f67a4343ef9e12141ad5e5fc84ef18f`; C87 admission records
`previous_review_findings: []`.

The C87 implementation was transplanted as six new C136 commits on top of the
planning main, without changing the historical C87 branch or its evidence.
After the disjoint checkpoint, refreshed `origin/main=
915ed982d23f2e549e529ff43c4f370b4b51e394` (including the accepted C127 merge)
was integrated without conflict as C136 merge commit
`73a587594a6e8c682bbe2755b14cb15897d4d853`:
the feature, contract/architecture alignment, quality repair, and three C87
evidence checkpoints. Startup integration
`8bdafc7f947a3a2c9856220abdc539437035bd21`, planning main
`bd6a1289218d1bef1a3af36e64e9d4496062416f`, and fresh-main ancestry all
verify. The candidate delta is limited to the admitted session-turn source,
tests, runtime service, coverage manifests, and C87 provenance tree; shared
registries remain untouched.

Post-merge focused implementation evidence at source `73a587594` passed:

- service session-turn normal tests: 42 tests in 3 packages, count 3;
- service session-turn race tests: 39 tests in 3 packages, count 3;
- deprecated CLI compatibility normal/race: 6/2 tests;
- separate external `GOWORK=off` consumer: pass;
- mutation, retirement/adapter, and owned/excluded-path verifiers: pass;
- credential-free bounded consumer runner: pass with one provider connection,
  two turns, copied audio/snapshot state, preserved invalid transition,
  isolated second service, and one close;
- bounded C136 public regression runner: both requested cases passed (three
  tests each), including the shipped credential-free audio/tool workflow and
  the existing interruption/tool continuation regression; output stayed under
  1 MiB and both child process groups were reaped with no survivors;
- formatting, focused vet, pinned staticcheck, pinned golangci-lint, coverage
  registration (188 packages/6 modules), changed-package coverage, Wire
  generation/check, and `git diff --check`: pass;
- accumulated normal and coverage session regressions: pass;
- accumulated race regressions: pass with an isolated Go build cache after an
  unrelated shared-cache import-file race in the first run.

The expected shared architecture gate remains blocked by the still-active
C110/C111/C112/C119 migration leases: `make architecture-size-check` reports
13 findings, specifically the generated sessionturns Wire file plus twelve
stale downward C87 baseline entries. C127 is complete and its accepted merge is
present; no shared registry/baseline file was edited. No current-head script
CI, independent review, guarded merge, vertical probe, or project acceptance
is claimed.

Exact next actions are to checkpoint/push this candidate, then retain C136
ownership while C110/C111/C112/C119 release the shared paths. After release,
fetch and integrate the accepted current main again, apply only the
demonstrated sessionturns registry/baseline entries permitted by the manifest,
rerun bounded focused/accumulated gates, and submit one changed head to script
CI without polling. Repair any exact C136 rejection on this same task.

## Exact-head causal and dependency recheck — 2026-09-13T16:13:18Z

Admission was reverified with `project-control.py verify-work --type task
--name audio-runtime-c136-recover-c87-session-turns`, returning `admitted` for
the sole `audio-runtime/audio-runtime-v1` project. The isolated branch remains
`codex/audio-runtime-c136-recover-c87-session-turns`, the worktree is clean, and
`origin/main=915ed982d23f2e549e529ff43c4f370b4b51e394` is the accepted C127
merge. Startup, planning-main, and current-main ancestry remain present; the
historical C87 ref and PR 476 remain unchanged at `28b5a9b18f67a4343ef9e12141ad5e5fc84ef18f`.

The exact candidate `4e67e8bce74a0c7b486d238912ff958d27e1540e` passed the
bounded causal recheck: sessionturns normal/race `42/39` tests at count 3,
deprecated CLI compatibility normal/race `6/2`, the separate `GOWORK=off`
consumer, all three valid C136 verifier modes, and both bounded public
credential-free audio/tool and interruption/tool cases (`3/3` tests each,
output bounded, reaped, no survivors). The accumulated
`COUNT=1 scripts/test-session-ci-regressions.sh all` matrix passed normal,
coverage, and race, including 20 high-rate trials and retained negative
controls. `make fmt`, focused vet, pinned staticcheck 2026.1, pinned
golangci-lint 2.9.0, `make wire-check`, `go list -deps`, and `git diff --check`
also pass.

Two prerequisite findings remain explicitly unresolved and outside this lease.
`make coverage-registration` fails only because the accepted C127 merge added
the unregistered package
`go-agent-runtime/services/session/internal/live/causal`; its manifest is not a
C136-owned path. `make architecture-size-check` fails with exactly 13 deferred
findings: the unregistered `sessionturns/wire/wire_gen.go` plus stale downward
entries for `ErrEmptyTurn`, `ErrInvalidTurnDirection`, `ErrInvalidTurnTick`,
`ErrMissingTurnInferencer`, `ErrSessionClosed`,
`ErrSessionEndedWithActiveTurn`, `ErrTurnAlreadyActive`,
`ErrTurnEndWithoutStart`, `ErrTurnMismatch`, `readTurnResponse`,
`TestSessionTurns_FiveTurnsUseOnePersistentSessionAndExactLifecycle`, and
`noTurnSetup`. No shared registry, architecture baseline, architecture policy,
or C127-owned path was edited. No current-head CI result, independent review,
guarded merge, vertical probe, or project acceptance is claimed.

Next action: retain C136 and route the C127 coverage-manifest residual to its
owner/primary while C110/C111/C112/C119 release their shared leases. Then fetch
and integrate the released accepted main, add only the demonstrated sessionturns
registry/baseline entries, rerun the bounded gates, and submit one changed head
to script CI without polling.

## Exact-head C136 script-CI rejection — run 34767963599

The first changed-head C136 script-CI run evaluated
e47a94277350c0e4e325a6a6da1758f57e32a5cb. Eight required checks passed: unit,
integration, coverage, race, hermetic, WebMCP Chrome, macOS audio release, and
Windows audio portable. The static job failed only at
make architecture-size-check with the following 13 deferred
shared/dependency findings:

- twelve stale downward C87 baseline entries for ErrEmptyTurn,
  ErrInvalidTurnDirection, ErrInvalidTurnTick, ErrMissingTurnInferencer,
  ErrSessionClosed, ErrSessionEndedWithActiveTurn, ErrTurnAlreadyActive,
  ErrTurnEndWithoutStart, ErrTurnMismatch, readTurnResponse,
  TestSessionTurns_FiveTurnsUseOnePersistentSessionAndExactLifecycle, and
  noTurnSetup;
- one unregistered generated-file finding for
  go-agent-runtime/services/sessionturns/wire/wire_gen.go.

The complete failed-step output is preserved in
ci-run-34767963599-static.log; the exact run metadata is preserved in
ci-run-34767963599.json. No C136-owned source or test failure was reported,
and no shared registry/baseline path was changed. This is not a CI-green,
review, merge, vertical-probe, or acceptance result. The next action remains
to retain C136 while C110/C111/C112/C119 release the shared paths and the
C127-owned coverage-manifest residual is resolved, then integrate the released
accepted main and apply only the demonstrated C136 registry/baseline changes.

## Exact-head owned recheck and current-main lease checkpoint — 2026-09-13T17:06:37Z

Admission remains `admitted` for `audio-runtime/audio-runtime-v1`; the branch
still matches `prd.json.branchName`, PR #513 remains open at
`d9278bf5d9c6a5ee514695a1139ecab66864e94a`, and the historical C87 branch/PR
476 remains unchanged at `28b5a9b18f67a4343ef9e12141ad5e5fc84ef18f`. A fresh
`git fetch origin main` reports `origin/main=b8650efd95f6a2e2e675a3dfd0969a9d311a877b`,
which contains the guarded C127 and C112 merges; it was not merged into this
candidate because C110, C111, and C119 still hold the shared registry/policy
leases. The worktree is clean and no predecessor or host checkout was reset.

The exact C136-owned recheck at `d9278bf5d` passed sessionturns normal/race,
deprecated CLI compatibility normal/race, the separate `GOWORK=off` consumer,
all three C136 verifier modes, and `go list -deps`. The bounded public runner
passed both requested cases (`3/3` tests each), bounded output, clean reaping,
and zero survivors. `COUNT=1 scripts/test-session-ci-regressions.sh all`
passed normal, coverage, and race, including all 20 high-rate trials and the
retained negative controls. `make fmt`, `make wire-check`, and `git diff
--check` passed; generated Wire output was unchanged.

The dependency gates remain fail-closed and unchanged: `make coverage-registration`
reports only the unregistered C127-owned package
`go-agent-runtime/services/session/internal/live/causal`, while
`make architecture-size-check` reports exactly the same 13 findings recorded
above (12 stale C87 entries and the unregistered sessionturns generated Wire
file). No C136 source/test failure, shared registry/policy edit, or new review
finding exists. This is evidence-only; it does not claim green CI, review,
merge, vertical validation, or acceptance.

Next action: retain C136 while C110/C111/C119 complete guarded merges and the
C127 coverage-manifest owner resolves its residual. Then fetch/integrate the
released accepted main while preserving the C136 sessionturns implementation,
apply only the demonstrated sessionturns registry/baseline entries, rerun the
bounded gates, commit/push the same PR, and submit its changed head to SCRIPT
CI without polling.

## Exact-head focused and accumulated recheck — 2026-09-13T17:33:43Z

Admission remains `admitted` for the sole `audio-runtime/audio-runtime-v1`
project, the isolated branch still matches `prd.json.branchName`, PR #513 is
open at `c2be50efdf1d571de504eda0ef6cee78f5ae1699`, and PR #476 remains
unchanged at `28b5a9b18f67a4343ef9e12141ad5e5fc84ef18f`. The fresh fetch keeps
`origin/main=b8650efd95f6a2e2e675a3dfd0969a9d311a877b`; C112 and C127 are
merged, while C110, C111, and C119 remain open and retain the shared-path
dependency. No source, predecessor, host checkout, or shared registry/policy
file was changed.

The exact current-head focused recheck passed sessionturns normal/race (`42` /
`39` tests), deprecated CLI compatibility normal/race (`6` / `2`), the
separate `GOWORK=off` consumer, and all three verifiers: mutations,
retirement-and-adapter, and owned-and-excluded-paths. The bounded public runner
passed both requested cases (`3` / `3` tests each) with scrubbed credentials,
bounded output, reaped children, and no surviving process groups. The first
attempt included an obsolete `--output-limit-bytes` option rejected by the
current runner parser; the supported rerun omitted only that unsupported option
and passed without source changes.

`COUNT=1 bash scripts/test-session-ci-regressions.sh all` passed normal,
coverage, and race modes, including all 20 high-rate trials and retained
negative controls. Expected replay/PCM/transcript mismatch diagnostics remained
asserted. These are executor checks only: the current candidate is not script-
CI green, independently reviewed, guarded-merged, vertically accepted, or
project-complete.

The required canonical factory work-list request returned
`CLI_COMMAND_FAILED` / `INTERNAL_SERVER_ERROR`; no factory state mutation was
attempted. GitHub and the preserved admission/evidence records independently
show no C136 review findings and the same C110/C111/C119 shared-path lease
dependency. Exact next action is to retain C136, then after those three guarded
merges release the paths, fetch and integrate the then-accepted `origin/main`,
add only the demonstrated sessionturns registry/baseline entries, rerun the
bounded gates, and submit the changed same-task head to SCRIPT CI without
polling.

## Fresh main movement without shared-lease release — 2026-09-13T17:35:44Z

After the focused checkpoint, a fresh `git fetch origin main` advanced
`origin/main` to `4a1c399ccbb3d780be95eb04316e84b8f11a6646`, containing the
guarded C118 merge. C110 PR #497, C111 PR #496, and C119 PR #505 remain open,
so the C136 shared Wire/architecture-policy lease is still unreleased. C136
did not merge this main revision, touch a shared path, or poll the in-flight
peer checks. The same-task next action is unchanged: after all three remaining
guarded merges release their paths, integrate the then-accepted main, apply
only the demonstrated sessionturns registrations/deletions, rerun bounded
gates, and submit the changed head to SCRIPT CI without polling.

## Exact-head CI rejection and bounded external-failure classification — 2026-09-13

PR #513 at head `7268f46a8c23cea480aecba621d49c01e4a30aac` entered script CI
as run `34772088096`. Raw run metadata and completed static/coverage job
metadata are preserved in `ci-run-34772088096.json`,
`ci-job-103763406509.json`, and `ci-job-103763406506.json`.

The static job `103763406509` completed failure in
`make architecture-size-check` with exactly 13 findings across 205
packages: the twelve stale C87 entries for `ErrEmptyTurn`,
`ErrInvalidTurnDirection`, `ErrInvalidTurnTick`,
`ErrMissingTurnInferencer`, `ErrSessionClosed`,
`ErrSessionEndedWithActiveTurn`, `ErrTurnAlreadyActive`,
`ErrTurnEndWithoutStart`, `ErrTurnMismatch`, `readTurnResponse`,
`TestSessionTurns_FiveTurnsUseOnePersistentSessionAndExactLifecycle`, and
`noTurnSetup`; plus the unregistered generated
`go-agent-runtime/services/sessionturns/wire/wire_gen.go`. The same job
subsequently completed vet, golangci-lint, and staticcheck successfully. The
exact failed-step output is preserved in
`ci-run-34772088096-static.log`; no C136 source/test finding was reported.

The coverage job `103763406506` independently failed on
`TestManagedBrowserManagerReusesStateAndClosesOnlyOnExplicitClose`: Go's
`TempDir` cleanup observed a non-empty WebMCP browser-profile directory.
That package/path is outside the C136 lease. The named test passed 10/10
normal focused repetitions and 5/5 focused coverage-mode repetitions in this
worktree, so this is a non-reproduced external failure, not a C136 repair
authorization. Its exact failure-step output is preserved in
`ci-run-34772088096-coverage.log`.

At inspection, unit, race, WebMCP Chrome, macOS audio release, and Windows
audio portable jobs were successful; integration and hermetic remained in
progress. No CI green, review, merge, vertical acceptance, or project
acceptance is claimed. The C136 worktree remains clean after the read-only
diagnosis. C110 PR #497, C111 PR #496, and C119 PR #505 are still open and
retain the shared registry/policy leases; current `origin/main` is
`4a1c399ccbb3d780be95eb04316e84b8f11a6646`. Local
`make coverage-registration` still reports only the unregistered
C127-owned package `go-agent-runtime/services/session/internal/live/causal`,
and local `make architecture-size-check` reproduces the same 13 findings.

Exact next action: retain C136 ownership without changing the WebMCP or C127
paths; after C110/C111/C119 complete guarded merges and release the shared
paths, fetch and integrate the then-accepted main, apply only the demonstrated
sessionturns Wire and downward baseline registrations/deletions, rerun focused
and accumulated causal gates, and submit one genuinely changed same-task head
to SCRIPT CI without polling.
