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
- Fresh matrix: `c60-final-20260911T145200Z`, 22/22 PASS in 69.298678 seconds, unchanged 900-second runner cap, six negative controls rejected.
- Accumulated lifecycle normal/race regressions: PASS.
- Exact-head evidence refresh commit `121984d47e32` reran normal/race in
  16.55s/8.47s, refreshed owned provenance/storage/index records, and
  `verify.py --mode all` passed before this evidence checkpoint; no matrix cell
  was rerun.
- Focused C60 attribution controls: one valid case accepted; ten invalid controls rejected.
- Classification: `NON_REPRODUCED` within the exact 22-cell matrix; the immutable source report remains FAILED and is not relabelled.
- Replay: immutable artifact-0/C16 software-file positive parity and real mutated-fixture rejection PASS in fresh run `replay-20260911T174336Z-20695` in 0.182598 seconds; positive exit 0, mutated-fixture exit 1, exact 4800-byte PCM and clean process groups; no credentials/live Realtime/native or acoustic claim.

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

The final local evidence gate is `verify.py --mode all`. The prior script-CI
static job `34613973921` tested head `efde4d49` and rejected the C47-owned
architecture baseline drift (`agent-cli/internal/transport/cli`: baseline 147,
current 148); all other static steps passed. No C60-owned path overlaps that
transport baseline. Do not resubmit this same candidate until C47/C55's repair
lands on accepted main, then resubmit this task to script CI. CI is not claimed
green and independent review remains separate. The current exact pushed head is
`121984d4`; its script-owned checks are not claimed green or polled here.
