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
- Focused C60 attribution controls: one valid case accepted; ten invalid controls rejected.
- Classification: `NON_REPRODUCED` within the exact 22-cell matrix; the immutable source report remains FAILED and is not relabelled.
- Replay: immutable artifact-0/C16 software-file positive parity and real mutated-fixture rejection PASS in run `replay-20260911T155154Z-33813` in 0.203247 seconds; positive exit 0, mutated-fixture exit 1, exact 4800-byte PCM and clean process groups; no credentials/live Realtime/native or acoustic claim.

The final local evidence gate is `verify.py --mode all`. The prior script-CI
run `34613973921` tested head `efde4d49` and rejected only the static baseline
drift plus the coverage/hermetic WebMCP composite-reference test. Those paths
belong to the separate C47 task; no C60-owned path overlaps them. Do not
resubmit this same candidate until C47's repair lands on main, then resubmit
this task to script CI. CI is not claimed green and independent review remains
separate.
