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
