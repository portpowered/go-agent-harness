# C87 implementation checkpoint

Admission was verified against the sole admitted `audio-runtime` project and
the exact `audio-runtime-v1` contract. The planning-main copy of the retired
CLI file is 243 physical production lines; this candidate keeps the CLI file
as a 40-line deprecated alias/delegation adapter.

The public `sessionturns` package contains the stable input, event, result,
error, and service contracts. The private `internal/service` package owns
state, transport protocol, copied snapshots, serialized event publication,
and bounded close. `sessionturns/wire` is the sole constructor edge.

Focused evidence currently passes:

- five-turn reuse, exact event ordering, copied text/audio input and response
  snapshots, invalid transition preservation, nonterminal error skipping,
  terminal error identity, cancellation, audio commit rejection, and single
  close behavior;
- normal and race tests for the extracted service, and the deprecated CLI
  compatibility adapter;
- the separate external consumer with `GOWORK=off`, including two independent
  Wire-built services;
- the credential-free bounded runner with 60-second child and 300-second
  aggregate limits.

The candidate has not claimed script CI, independent review, guarded merge,
primary vertical acceptance, or project acceptance. The shared Wire registry
and architecture-size baseline remain untouched while their active C79 lease
is unreleased; they are the next integration action after the exact lease
release and accepted-main refresh.

## Static-gate repair checkpoint

CI run `34686814971` evaluated source head `8fb7536f06f238847d6ea863a60548788e631947`.
Unit, race, WebMCP Chrome, macOS audio release, and Windows portable checks
passed. Static reported two C87-owned `contextcheck` findings in the deprecated
CLI compatibility fixture, plus one unregistered
`go-agent-runtime/services/sessionturns/wire/wire_gen.go` and twelve stale
downward baseline entries for the retired CLI symbols. The latter thirteen
findings are shared C79 ownership and remain deferred.

Commit `243865ef280ccfd416fd9c6fb9c7dec378b7949c` repairs the owned findings by
propagating the inherited send context, handling every typed send outcome,
preserving the close error when a connection loses a close race, and checking
test helper results and type assertions. On that source revision, pinned
`make lint` reports 0 issues in all 15 modules, `make staticcheck` passes,
`make coverage-registration` checks 179 packages, service normal/race tests
pass 48/48, CLI compatibility normal/race passes 6/2, the external
`GOWORK=off` consumer passes, all three verifier modes pass, the credential-free
consumer program/tests pass within 60-second child and 300-second aggregate
limits, and `COUNT=1 scripts/test-session-ci-regressions.sh all` passes normal,
coverage, and race modes. `make architecture-size-check` still reports exactly
the same 13 shared Wire/baseline findings; no shared file was changed.

This is executor evidence only. The repaired head is not yet submitted to the
script CI gate because C79's reviewed guarded merge has not released the shared
registry and architecture-baseline lease. After that release, fetch accepted
main, apply only the C87 Wire registration and demonstrated downward/deletion
baseline edits, rerun the bounded gates, and submit the changed same-task PR.

## Exact-head CI rejection and bounded recovery

At `2026-09-12T10:42:58Z`, PR `#476` was still at clean head
`3e7da39c8e7173a59167bb0976a80248da0f387b`. Script CI run `34687481090`
evaluated that exact head. Unit, race, coverage, hermetic, WebMCP Chrome, macOS
audio release, and Windows portable software passed. The two failures were
preserved without relabeling:

- Static job `103536897303` reported the unregistered generated file
  `go-agent-runtime/services/sessionturns/wire/wire_gen.go` and exactly twelve
  stale downward-only architecture entries: `ErrEmptyTurn`,
  `ErrInvalidTurnDirection`, `ErrInvalidTurnTick`, `ErrMissingTurnInferencer`,
  `ErrSessionClosed`, `ErrSessionEndedWithActiveTurn`, `ErrTurnAlreadyActive`,
  `ErrTurnEndWithoutStart`, `ErrTurnMismatch`, `readTurnResponse`,
  `TestSessionTurns_FiveTurnsUseOnePersistentSessionAndExactLifecycle`, and
  `noTurnSetup`. These are the shared C79 registration/baseline lease, so C87
  made no shared-file edit. C79 remains `ci-pending` on open PR `#470` with no
  guarded merge/release.
- Integration job `103536897232` failed only the known C64-owned
  `TestAgentBinaryTest46HighRateToolAudioRegression/trial_13`: the remote
  device rendered `167991/174391` compared samples, losing exactly `6400`
  (`96.3%` retained), while reporting zero dropped samples, overflow events,
  discarded samples, or discard events. C87 did not edit C64 transport/device
  or drain paths.

The bounded same-head recovery checks passed: sessionturns normal and race
matrices each ran `48` tests; deprecated CLI compatibility normal and race each
ran `6` tests; the separate `GOWORK=off` consumer passed; all three verifier
modes passed; the credential-free consumer test and program passed with
`connections=1`, `history=2`, `second_connections=1`, `second_history=1`,
`audio_copied=true`, `snapshot_copied=true`, `invalid_transition_preserved=true`,
`isolated=true`, and `closed=true`; coverage registration checked `179` packages;
and `COUNT=1 scripts/test-session-ci-regressions.sh all` passed normal,
coverage, and race modes. Its local high-rate test46 control passed all `20`
trials. These checks do not claim script-CI green, independent review, merge,
vertical acceptance, or project acceptance.

Exact next action: after C79's reviewed guarded merge releases the shared lease,
fetch accepted main, apply only the sessionturns Wire registration and the
demonstrated twelve downward baseline deletions, rerun the bounded Wire,
architecture, focused causal, and accumulated regression checks, commit and
push the same PR `#476`, and submit the changed head to script CI without
polling. Preserve the C64 high-rate failure as dependency evidence and retain
C87 ownership for any actionable same-task rejection.

## Current-main dependency recheck

The isolated branch remains clean and pushed at `eba38db1c450e0679456536f2a5d6da3cff6c4ff`;
`git fetch origin main` refreshed `origin/main` to
`3d3e72786ac6fc1fd47c7e029589e5117674b035`. Startup integration and planning
main ancestry still pass, while current-main ancestry remains intentionally
open until C79's reviewed guarded merge releases the shared lease. PR `#476`
remains open at the same head. The sole admitted project and task identity
remain admitted; `admission.json` records no previous reviewer findings, and
GitHub has no C87 review row.

The completed exact-head PR run `34689223740` at `eba38db1` was read from the
full failed log. Unit, integration, coverage, race, hermetic, WebMCP Chrome,
macOS audio release and Windows portable checks pass. Static fails only on the
same C79-owned set: the unregistered
`go-agent-runtime/services/sessionturns/wire/wire_gen.go` plus the twelve
downward/stale entries listed above. No C87-owned source defect or C64
integration failure was reported by this newer run; the earlier C64 high-rate
failure remains preserved historical dependency evidence.

Fresh bounded rechecks on `eba38db1` pass: sessionturns normal/race (45/42
tests), CLI compatibility normal/race (6/2), the separate `GOWORK=off`
consumer, all three verifier modes, the credential-free runner, and
`COUNT=1 scripts/test-session-ci-regressions.sh all` in normal, coverage and
race modes. `make coverage-registration`, pinned staticcheck and focused vet
pass. `make wire-check` and `make architecture-size-check` fail only with the
same shared registration/baseline findings. The pinned local lint also reports
one pre-existing C64-owned `rtc_device_runtime_test.go:275` blank-assignment
finding; that file is unchanged by C87 and the fetched current main contains
C64's `//nolint:errcheck` repair. No shared or peer path was edited.

This remains an executor checkpoint, not CI-green, review, merge, vertical or
project acceptance. Exact next action is unchanged: after C79's reviewed
guarded merge, integrate the then-accepted main, apply only C87's generated
Wire registration and twelve downward/deletion-only baseline changes, rerun
the bounded gates, commit/push the same PR and submit its changed head to
script CI without polling.
