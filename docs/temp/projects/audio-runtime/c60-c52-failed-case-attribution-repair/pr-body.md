# C60: repair failed-case attribution for C52

## Summary

Repair the narrow C52 evidence-control defect identified by the immutable
corrected vertical probe: a fresh independent run had one clean process exit
1 for `pr438/order-timeout-first/empty_response`, but the old verifier inferred
failure from absence of `test_outcome` and rejected the preserved direct
`test_outcome` trace shape.

This change keeps the task evidence-only. It adds raw selected Go terminal
actions and exact identity bindings, propagates run identity into trace events,
and attributes a failed case only when one selected raw `Action=fail` maps to
one trace group. The direct and deferred trace shapes remain supported; exit
alone, duplicate/multiple/transformed actions, wrong package/test, stale or
cross-run identity, and changed peer boundaries fail closed.

## Gate evidence

- Admission: `audio-runtime-c60-c52-failed-case-attribution-repair`, branch `codex/audio-runtime-c60-c52-failed-case-attribution-repair`.
- Fresh matrix: `c60-final-20260911T212525Z`, 22/22 PASS in 69.671585 seconds
  under the 900-second runner cap; all six negative controls were rejected.
- Accumulated lifecycle normal/race regressions: PASS (24.157 seconds total).
- Final exact-head evidence refreshed the owned matrix, regression,
  provenance/storage/index records, and replay report. `verify.py --mode all`
  passes with the current exact evidence.
- Focused C60 attribution controls: one valid case accepted; ten invalid controls rejected.
- Classification: `NON_REPRODUCED` within the exact 22-cell matrix; the immutable source report remains FAILED and is not relabelled.
- Replay: immutable artifact-0/C16 software-file positive parity and real
  mutated-fixture rejection PASS in fresh run `replay-20260911T213009Z-85020`
  in 0.178985 seconds; positive exit 0, mutated-fixture exit 1, exact
  4800-byte PCM (`0e769b4a...`) and clean process groups; no credentials/live
  Realtime/native or acoustic claim.

## Bounded PR439 occurrence attribution

The combined PR439 coverage job `103345550039` at head `e0d4ba24` failed only
the separate `agent-cli/internal/transport/cli/internal/events` timeout fixture
at `room_live_liveness_test.go:40`, after its own 3-second `waitForLiveness`
guard. The complete job-log SHA-256 is recorded in
`ci-room-liveness-attribution.json`. The exact test passes with raw Go
`Action=pass` under both hermetic normal and coverage instrumentation on C60
head `79754c85`; the hosted log has no C52 action/trace identity. This remains
`FAIL_CLOSED_UNRESOLVED`, not a C52 recurrence, report relabel, production
repair, or acceptance waiver; C47/C55 own the transport fixture follow-up.
The same bounded normal/coverage control was refreshed on evidence checkpoint
`c4d17b1e`: both selected-test and package actions were `pass`, with 0.284s
normal package time and 0.426s coverage package time at 27.7%; the three
transport/events source-file hashes are unchanged from the preserved control.

The final local evidence gate is `verify.py --mode all`. The prior script-CI
static job `34613973921` tested head `efde4d49` and rejected the C47-owned
architecture baseline drift (`agent-cli/internal/transport/cli`: baseline 147,
current 148); all other static steps passed. No C60-owned path overlaps that
transport baseline. C47/C55's accepted integration is now present in
`origin/main` at `d5d6f843` and in this branch's merge checkpoint
`b53144eb`. This changed head is ready for script CI. CI is not claimed green
and independent review remains separate. The current exact PR head and its
evidence binding are recorded in `provenance.json`; the script-owned checks are
not claimed green or polled here. No review, merge, or live/native probe
acceptance is claimed.
