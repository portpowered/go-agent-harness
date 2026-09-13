# C82-06 terminal recovery and rejection repair checkpoint

Candidate repair is based on `27bc7e1f598c5e23aef08fcb9af01817011fecda`
(`feat: extract live session orchestration service`) and remains on the
admitted branch. The pre-repair candidate reproduced the historical coverage
failure: the paced three-frame audio oracle failed 8/50 times without
coverage and 4/30 times with coverage. The detached predecessor at
`27bc7e1f^` passed the same oracle 50/50.

The repair keeps caller-owned finite input producers alive through the bounded
provider straggler drain, then cancels and joins input before owned-resource
cleanup. The CLI adapter opts process-owned, no-send sources into input
cancellation immediately after upstream quiescing, preserving their prior
termination behavior without cancelling caller-owned finite producers. This
preserves the extracted service's one terminal boundary and keeps both input
classes within the same bounded cleanup path. Service regression tests assert
both cancellation orderings.

Current focused evidence:

- service cancellation-order test: normal 20/20, race 5/5;
- process-owned early-cancellation service ordering assertion: pass;
- paced three-frame audio oracle: normal 50/50 and coverage-instrumented
  50/50;
- focused audio cancellation control: normal 20/20, race 10/10;
- C82 service package: normal and race pass;
- full agent-runtime package: normal pass; focused C82 race regressions pass;
- independent external consumer: normal and race pass with `GOWORK=off`;
- `make fmt`, `make vet`, pinned golangci-lint, pinned staticcheck, and
  coverage registration (179 packages across 6 modules) pass;
- behavior matrix, both mutation controls, and retirement-and-scope pass;
- retirement remains `session_live.go` 579 + `session_live_setup.go` 54,
  total 633, with 500 lines retired.

The exact rejected findings are repaired: the three unchecked test closes and
the magic timeout are gone, and the now-unreferenced
`flushBufferedSessionLoopMessages` helper is deleted after proving there are
no callers. The evidence verifier's allow-list names that one explicitly
authorized legacy path exactly; no broad scope was added.

Shared-lane prerequisites remain unmodified and are not waived:

- `make wire-check` reports only the unregistered generated file
  `go-agent-runtime/services/sessionlive/wire/wire_gen.go`; the exact
  `scripts/wire-packages.txt` writer remains leased to C79.
- `make architecture-size-check` reports the ten expected reduced/stale
  `session_live.go` entries plus the newly reduced `session_drain.go` file
  baseline; `docs/architecture/architecture-size-baseline.json` remains
  leased to C79.

A broad local race sweep also timed out in the unchanged one-second browser
validator test; the focused C82 race set is green and no unrelated test or
fixture was changed.

Next action: commit and push this disjoint repair on the same branch/PR. After
C64's terminal-drain work and C79's shared-lane lock release reconcile on
accepted main, apply only the generated Wire registration and demonstrated
downward/stale baseline entries, rerun both shared gates, and submit the same
PR to script CI without polling it.

## Current-owner revalidation — 2026-09-12T09:18:07Z

The admitted branch remains clean and pushed at `d6f788a5d487b1a65a1255845cf6066b02d98120`, with freshly fetched `origin/main` and the required startup/accepted-main ancestry intact. The bounded causal revalidation passes:

- sessionlive causal normal tests pass `50/50`; the selected service race set
  passes `30/30`;
- CLI live/scheduled/response causal selectors pass `228/228`;
- behavior-matrix and retirement-and-scope verification pass, with the exact
  `579 + 54 = 633` adapter count and `500` retired lines;
- post-Done-drain mutation fails at the missing final delta assertion, and
  deadline-cleanup mutation fails at the timer-stop assertion;
- accumulated `scripts/test-session-ci-regressions.sh all` passes in normal,
  coverage, and race modes, including the high-rate audio/tool, scheduled,
  replay-negative, simulated-device, and composed-provider controls.

The current shared-gate observations remain unmodified and actionable: Wire
reports only the unregistered `go-agent-runtime/services/sessionlive/wire/wire_gen.go`; architecture reports 11 downward/stale entries, consisting of the reduced `session_drain.go` baseline and the 10 reduced/stale `session_live.go` entries. C79 still owns the shared Wire registry and architecture baseline, and C64's reviewed terminal-drain release is still required before final main reconciliation. No CI-green, review, merge, vertical, acoustic, or project-acceptance claim is made.

## Fresh bounded revalidation — 2026-09-12T09:40:38Z

At source `9eac836b4f92fe8151495475fc190550989aa74c`, the focused causal and
accumulated controls were rerun without changing the shared C64/C79 paths:

- `go test ./go-agent-runtime/services/sessionlive/... -count=5` passed 70
  tests across 3 packages; the selected service race run passed 39 tests across
  3 packages; the selected CLI live/scheduled/response run passed 267 tests.
- The GOWORK=off external consumer passed normal and race at `-count=3`.
- `verify.py --mode behavior-matrix` and `--mode retirement-and-scope` passed;
  the latter rechecked the 1,061 + 72 baseline, current 579 + 54 adapter,
  and 500 retired lines.
- Both mutation controls passed discovery and positive behavior, then failed
  for the intended causal assertions: the post-Done mutation reported the
  missing final text delta, and the deadline-cleanup mutation reported that
  the deadline timer was not stopped.
- `COUNT=3 scripts/test-session-ci-regressions.sh all` exited 0 in normal,
  coverage, and race modes. CLI transport, shipped integration/high-rate and
  replay-negative controls, simulated-device controls, and composed-provider
  controls all passed, including the expected negative-control diagnostics.

This remains executor evidence only. C64 terminal-drain ownership and C79
shared Wire/architecture ownership are still unreleased; the generated Wire
registration and 11 demonstrated downward/stale architecture entries remain
deferred. No script-CI, independent-review, guarded-merge, vertical, or
project-acceptance result is claimed.

## Current-head bounded causal and accumulated revalidation — 2026-09-12T09:58:38Z

At exact source `fe3dd7af3e84529303aba42b31ba738f034e9ba8`, the requested
focused and accumulated controls pass without touching the C64/C79 leased
files:

- `go test ./go-agent-runtime/services/sessionlive/... -count=5` passed 70
  tests across 3 packages; the selected service race run passed 42 tests
  across 3 packages.
- The selected CLI live/scheduled/response run passed 264 normal tests and
  88 race tests.
- The GOWORK=off external consumer passed normal and race at `-count=3`.
- `verify.py --mode behavior-matrix` and `--mode retirement-and-scope` passed;
  the exact current adapter remains `579 + 54 = 633` lines with 500 retired.
- Both mutation controls discovered and passed the positive tests, then failed
  at their intended causal assertions: post-Done drain reported the missing
  final text delta, and deadline cleanup reported the timer was not stopped.
- `COUNT=3 scripts/test-session-ci-regressions.sh all` exited 0 in normal,
  coverage, and race modes. The expected replay/PCM/transcript negative
  diagnostics remained rejected, while high-rate audio/tool, scheduled,
  simulated-device, and composed-provider controls passed.

This is executor evidence only. C64 PR459 and C79 PR470 remain open, so their
reviewed releases and a fresh accepted-main reconciliation are still required
before applying the one generated-Wire registration and the 11 demonstrated
downward/stale baseline changes. No script-CI, independent-review,
guarded-merge, vertical, or project-acceptance result is claimed.

## Current-owner revalidation — 2026-09-12T10:32:41Z

The clean pushed head remains `ceb3a09d4e40bff70d5adcc4fd1970a888b52275`.
Fresh bounded checks pass without touching the C64/C79 leased files:

- sessionlive normal count=5 passed 70 tests across 3 packages; the package
  race run count=3 passed 42 tests across 3 packages;
- the selected CLI live/scheduled/response run passed 264 normal tests and 88
  race tests; the GOWORK=off external consumer passed normal and race at
  count=3;
- behavior-matrix and retirement-and-scope passed, with the exact current
  adapter at `579 + 54 = 633` lines and 500 retired;
- both mutation controls discovered and passed the positive tests, then failed
  only at the intended missing-final-delta and unstopped-deadline assertions;
- `COUNT=3 scripts/test-session-ci-regressions.sh all` exited 0 in normal,
  coverage, and race modes. The expected replay/PCM/transcript negative
  diagnostics remained rejected, while high-rate audio/tool, scheduled,
  simulated-device, and composed-provider controls passed;
- `make fmt`, `make vet`, and `git diff --check` passed. Required startup,
  accepted-main, and fetched `origin/main` ancestry still pass, with
  `origin/main=84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f`.

The local shared gates remain intentionally red only for the known deferred
work: Wire reports the unregistered C82 generated file, and architecture
reports the one reduced `session_drain.go` entry plus ten reduced/stale
`session_live.go` entries. C64 PR459 and C79 PR470 are still open; no shared
file was edited and no prerequisite was waived. This remains executor
evidence only: no script-CI, independent-review, guarded-merge, vertical, or
project-acceptance result is claimed.

Next action: after the reviewed C64 terminal-drain and C79 shared-file merges
release their leases, fetch and reconcile accepted main on this same branch,
apply only the C82 generated-Wire registration and its 11 demonstrated
downward/stale baseline changes, rerun the bounded gates, commit/push the same
PR474, and submit its changed head to script CI without polling.

## Accepted-C64 ancestry revalidation — 2026-09-12T10:44:09Z

C64 PR459 is now merged at `d23f554b97f0dbc3ebcbf5193c23514c09b7e388` and
fresh `origin/main=59af6325614d80173447fe2018a0471e27b4e7b1` was merged into
the clean C82 branch as `3262145503e94107a1b8de20db52a4dfe65f73d8`. The merge
preserved the C82 extraction and all required startup, accepted-main, and
current-main ancestry; the host checkout was not merged or reset.

Post-merge causal checks pass: sessionlive normal count=5 passed 70 tests
across 3 packages; the selected service race count=3 passed 42; selected CLI
live/scheduled/response passed 264 normal and 88 race; and the GOWORK=off
external consumer passed normal and race count=3. Behavior-matrix and
retirement-and-scope passed at `579 + 54 = 633` adapter lines with 500
retired. Both mutation controls passed discovery and positive behavior, then
failed only at the intended missing-final-delta and unstopped-deadline
assertions. `COUNT=3 scripts/test-session-ci-regressions.sh all` exited 0 in
normal, coverage, and race modes after the C64 merge; expected replay/PCM/
transcript negatives and all high-rate audio/tool, scheduled, simulated-device
and composed-provider controls remain intact. `make fmt`, `make vet`, and
`git diff --check` pass.

The merged-head shared gates remain intentionally unresolved only because C79
PR470 is still open: `make wire-check` reports exactly the unregistered C82
`go-agent-runtime/services/sessionlive/wire/wire_gen.go`, and
`make architecture-size-check` reports exactly 11 entries (one reduced
`session_drain.go` entry plus ten reduced/stale `session_live.go` entries).
No shared file was edited. No script-CI, independent-review, guarded-merge,
vertical, or project-acceptance result is claimed.

Next action: when C79's reviewed guarded merge releases both shared-file
leases, fetch current main again, reconcile it on this same branch, apply only
the C82 generated-Wire registration and these 11 demonstrated downward/stale
entries, rerun the bounded gates, push/update PR474, and submit the changed
head to script CI without polling.
