# C60 failed-case attribution repair

This retained C52 evidence directory is the matrix and lifecycle-test input for
the admitted task `audio-runtime-c60-c52-failed-case-attribution-repair`. It
preserves the fixed PR438 revision
`823bd350fe5d11782c38bda87d7b7bfd7d89d7cd`, planning revision
`7f73c8b3b4ebc99b55b8bb5e802beff024385407`, frozen 11-cell matrix, prior CI
log, canonical archives, and predecessor checkpoints. It does not modify
production or tracked test sources.

The repaired runner records raw Go terminal actions for every selected
parent/subtest, binds those actions to revision, run ID, matrix cell, full Go
package, and test identity, and carries the same run identity into every trace
event. The verifier now attributes a failed process only when exactly one
selected raw `Action=fail` is bound to exactly one trace case. It accepts the
preserved direct `test_outcome` failure shape and retains the deferred-outcome
shape for assertion-abort cases; process exit alone, duplicate/multiple or
transformed actions, identity changes, cross-run traces, and changed peer
boundaries fail closed.

Fresh matrix evidence is run
`c60-final-20260911T145200Z`: both fixed revisions, all 22 cells, and all six
runner negative controls passed in 69.298678 seconds under the existing
900-second aggregate cap. Normal/race lifecycle regressions, storage/cleanup,
overlay, selection/no-retry, and seven integrity controls pass. The resulting
classification is `NON_REPRODUCED` within the declared matrix; it does not
relabel the immutable C52 source report or claim hosted CI is green.

The task-local C60 fixture and focused controls are in the sibling
`c60-c52-failed-case-attribution-repair/` directory. Its bounded replay uses
the immutable staged artifact-0 and exact credential-free C16/config inputs;
the positive replay preserves the marker, continuation, exact 4800-byte PCM,
session-log hash, and clean process group, while a mutated fixture is rejected.
No live Realtime provider, native endpoint, or acoustic claim is made.

Run the final local evidence gate with:

```sh
rtk proxy python3 docs/temp/projects/audio-runtime/audio-runtime-c52-hermetic-room-liveness-characterization/verify.py --mode all
```

The candidate is handed to script CI and independent review after commit/push;
this evidence does not assert either result.
