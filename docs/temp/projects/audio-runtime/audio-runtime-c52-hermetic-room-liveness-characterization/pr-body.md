# C60: repair C52 failed-case attribution

## Summary

The admitted C60 task repairs the C52 evidence verifier that rejected the
preserved direct `test_outcome` shape for the independent
`pr438/order-timeout-first/empty_response` observation. The repair is limited
to the C52 evidence runner/verifier/overlay and the C60 evidence directory;
production and tracked test sources are unchanged.

The runner now retains raw Go `Action=pass|fail|skip` records for each selected
parent/subtest and binds them to the fixed revision, run ID, matrix cell, full
package, parent test, and subtests. The overlay carries the run ID into every
trace event and records exact case parent/subtest identity. Attribution is
accepted only for one selected raw failed action mapped to one trace group;
exit status, transformed checkpoints, duplicates, multiple failures, stale or
cross-run identities, and changed peer boundaries are rejected.

## Evidence

- Fresh run `c60-final-20260911T145200Z`: 22/22 cells pass across PR438 and planning main in 69.298678 seconds; aggregate cap remains the existing 900 seconds.
- Six matrix negative controls reject ambient environment forwarding, zero/wrong selection, survivors, and archive reuse mismatch.
- Normal and race lifecycle regressions pass; cleanup, storage, overlay, no-retry/selection, and seven integrity controls pass.
- C60 focused attribution controls accept one valid direct failed case and reject all ten declared invalid variants.
- Classification is `NON_REPRODUCED` only because all 22 fresh cells pass; the immutable corrected source report remains preserved as FAILED and is not relabeled.
- The C60 replay wrapper passes the exact immutable artifact-0/C16 software-file replay and rejects a real mutated fixture, with no credentials or live Realtime use.

## Review and handoff

The candidate preserves the fixed comparison ancestry and only changes the
admitted C52/C60 evidence paths. `verify.py --mode all` is the final local
gate. After commit and push, submit this same task to script CI; CI status and
independent review remain external handoff steps.
