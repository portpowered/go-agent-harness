# C153 evidence ledger

Task: `audio-runtime-c153-repair-sessiontrace-close-retry`
Project: `audio-runtime`; contract: `audio-runtime-v1`; factory session: `~default`

This ledger records bounded executor evidence for the causal sessiontrace close
retry repair. C153 is a baseline repair slice only; TRACE, REPLAY, FAILURES,
QUALITY and PARITY remain open, and C143 must later integrate any guarded merge
and rerun its own changed-head CI/review/probe.

## Admission and source before mutation

- `project-control.py verify-work --type task` returned `{"status":"admitted","project":"audio-runtime","name":"audio-runtime-c153-repair-sessiontrace-close-retry"}`.
- `prd.json.branchName` is `codex/audio-runtime-c153-repair-sessiontrace-close-retry`.
- Initial source was accepted `origin/main` `4a1c399ccbb3d780be95eb04316e84b8f11a6646`.
- Required startup integration revision `8bdafc7f947a3a2c9856220abdc539437035bd21` is an ancestor.
- Initial worktree was clean; only the two owned service files and this evidence directory are in scope.

## Failing-before

The exact one-timeout-then-release-then-immediate-retry sequence is captured
below before implementation changes. The normal and hermetic runs must retain
their exact command, exit status, and first observed failure/non-reproduction.

- Initial uninstrumented normal profile, `rtk go test ./go-agent-runtime/services/sessiontrace/internal/service -run '^TestFinishCloseTimeoutRetainsStagedPath$' -count=50 -timeout=180s`, exited `0` (`50 passed`); this was a non-reproduction at accepted main and was not treated as a pass for the causal story.
- Deterministic normal characterization after adding only the owned test handshake, `rtk go test ./go-agent-runtime/services/sessiontrace/internal/service -run '^TestFinishCloseTimeoutRetainsStagedPath$' -count=1 -timeout=180s`, exited `1`. The first failure was `service_test.go:101: retry returned before the in-flight close completed: session trace close timed out ... audio evidence retained at <temporary staged path>`. This is the pre-repair oracle: the first bounded timeout retained the staged path, release was signaled, and the immediate retry applied a second timeout before the same close completed.
- Deterministic hermetic characterization, `rtk proxy env CGO_ENABLED=0 go test ./go-agent-runtime/services/sessiontrace/internal/service -tags=nomicrophone -run '^TestFinishCloseTimeoutRetainsStagedPath$' -count=1 -coverprofile=docs/temp/projects/audio-runtime/audio-runtime-c153-repair-sessiontrace-close-retry/failing-before.cover.out -timeout=180s`, exited `1` in `0.172s`. It reported `service_test.go:101: retry returned before the in-flight close completed: session trace close timed out after 1ms`, retained staged path `/var/folders/p0/39h9prbs7pn446h35_zhpr300000gn/T/TestFinishCloseTimeoutRetainsStagedPath1013040211/001`, and `coverage: 10.0% of statements`.

The temporary handshake is now part of the owned behavioral regression: it
waits for closeTrace to start, releases its first blocking gate, holds the
completion gate, and observes whether an immediate retry waits or returns a
second timeout. It uses bounded context/time guards and no sleep/retry loop.

## Repair and focused proof

The service now records whether the `sync.Once` callback was started by the
current caller. That first caller retains the configured close-timeout select;
later callers wait for the same `closed` completion or their caller context,
without invoking `closeTrace` again or applying a second close timeout.

- Normal focused repair check, `rtk go test ./go-agent-runtime/services/sessiontrace/internal/service -run '^TestFinishCloseTimeoutRetainsStagedPath$' -count=50 -timeout=180s`, exited `0` (`50 passed`).
- Hermetic focused repair check, `rtk proxy env CGO_ENABLED=0 go test ./go-agent-runtime/services/sessiontrace/internal/service -tags=nomicrophone -run '^TestFinishCloseTimeoutRetainsStagedPath$' -count=50 -coverprofile=docs/temp/projects/audio-runtime/audio-runtime-c153-repair-sessiontrace-close-retry/passing-after.cover.out -timeout=180s`, exited `0` (`50 passed`, coverage `15.1%`).
- Focused accumulated normal check, `rtk go test ./go-agent-runtime/services/sessiontrace/internal/service -run 'Finish.*(Close|Timeout|Cancel|Retain|Unpublished)|PreparedCapturesEdgesRedactsAndPublishes' -count=50 -timeout=240s`, exited `0` (`350 passed`).
- Focused accumulated race check, `rtk go test -race ./go-agent-runtime/services/sessiontrace/internal/service -run 'Finish.*(Close|Timeout|Cancel|Retain|Unpublished)|PreparedCapturesEdgesRedactsAndPublishes' -count=20 -timeout=420s`, exited `0` (`140 passed`).

The focused tests include the existing no-overwrite, publication, PCM-copy and
credential-redaction workflow. Full-package and final clean-source checks remain
to be recorded below.

## Accumulated package proof

- Full package normal, `rtk go test ./go-agent-runtime/services/sessiontrace/... -count=1 -timeout=300s`, exited `0` (`15 passed in 3 packages`).
- Full package race, `rtk go test -race ./go-agent-runtime/services/sessiontrace/... -count=1 -timeout=420s`, exited `0` (`15 passed in 3 packages`).
- Hermetic package coverage, `rtk proxy env CGO_ENABLED=0 go test ./go-agent-runtime/services/sessiontrace/... -tags=nomicrophone -count=1 -coverprofile=docs/temp/projects/audio-runtime/audio-runtime-c153-repair-sessiontrace-close-retry/sessiontrace.cover.out -timeout=300s`, exited `0`: public package `0.0%`, internal service `90.4%`, Wire `100.0%`.
- Existing publish/redaction workflow, `rtk go test ./go-agent-runtime/services/sessiontrace/internal/service -run '^TestPreparedCapturesEdgesRedactsAndPublishes$' -count=5 -timeout=180s`, exited `0` (`5 passed`).
- Targeted vet, `rtk go vet ./go-agent-runtime/services/sessiontrace/...`, exited `0` (`No issues found`).
- `rtk git diff --check`, exited `0`.

## Checkpoint and ancestry

- Bounded repair checkpoint: `2b86e658e1185aebbc693de252f14c502a6f43ca` (`fix(sessiontrace): wait for in-flight close retry`).
- After `rtk git fetch origin main`, `HEAD` is `2b86e658e1185aebbc693de252f14c502a6f43ca` and `origin/main` is `4a1c399ccbb3d780be95eb04316e84b8f11a6646`; `origin/main` is an ancestor of `HEAD`.
- The required startup integration revision `8bdafc7f947a3a2c9856220abdc539437035bd21` and baseline revision `3194edd97aed588f7cdf2f8c58a69ac21da4c9ad` are ancestors of `HEAD`.
- The pushed candidate must remain limited to the two owned service files and this evidence directory. No current-head SCRIPT CI, independent review, guarded merge, C143 recovery, vertical acceptance or project completion is claimed here.

## Script-CI handoff

- Final pushed source head before handoff: `ed5dc6ed0cdf178682408f2e166c5e10bfc098c8`.
- Pull request opened against `main`: [PR #515](https://github.com/portpowered/go-agent-harness/pull/515).
- The branch is clean and tracks the pushed exact head. `ACCEPTED` hands this
  candidate to the script-owned current-head CI gate; CI has not been polled or
  claimed green. Fresh independent review and guarded merge remain external
  gates, and C143 must later integrate this guarded-merged repair on its own
  changed head.

## Fresh executor recheck

At `2026-09-13T21:35:29Z`, after fetching `origin/main`, the candidate remained
clean at `1c044cd6f1d76db2015527b863e88cba16f32c76`; accepted `origin/main`
`4a1c399ccbb3d780be95eb04316e84b8f11a6646` and startup integration
`8bdafc7f947a3a2c9856220abdc539437035bd21` remained ancestors, and the remote
branch resolved to the same head before this evidence-only checkpoint.

- Causal retry, `rtk go test ./go-agent-runtime/services/sessiontrace/internal/service -run '^TestFinishCloseTimeoutRetainsStagedPath$' -count=50 -timeout=180s`, exited `0` (`50 passed`).
- Accumulated focused normal and race checks exited `0` (`350` and `140` passed).
- Full sessiontrace normal and race checks exited `0` (`15` passed in 3 packages each).
- Hermetic `nomicrophone` package coverage exited `0` (`90.4%` internal service, `100.0%` Wire); the fresh coverprofile was written outside the repository.
- Publish/redaction exited `0` (`5 passed`), targeted vet reported no issues, and `git diff --check` exited `0`.

No implementation or unowned path changed after the bounded repair. This
recheck adds no CI, review, merge, vertical, hardware/acoustic or project
completion claim; the exact pushed candidate remains for script CI and fresh
independent review.
