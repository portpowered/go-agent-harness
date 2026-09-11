# C52 hermetic room-liveness characterization

This directory is the sole C52 executor lease. It contains an evidence-only
comparison of PR438 (`823bd350fe5d11782c38bda87d7b7bfd7d89d7cd`) and the
planning-time integrated main (`7f73c8b3b4ebc99b55b8bb5e802beff024385407`).
The final tested evidence parent is the clean candidate merge
`3af52f8bb74850bbc3d7102ea4302b6b2b9a3401`, which includes refreshed
`origin/main` (`4a3f83430be6fde42ef3b25d39e25a9492980e30`) as an ancestor.
The final handoff commit is an evidence-only descendant of that tested source;
the executable and test inputs are unchanged.
The fetched `origin/main` may advance; its exact revision is recorded in
`provenance.json` and does not replace the declared comparison input.

The matrix is frozen in `matrix.json` before `run.py` executes. `run.py` makes
clean git archives, applies only the generated scratch overlay, runs one
bounded first-result-per-cell matrix, and removes its known scratch trees and
run-local caches after child groups are reaped. It never checks out, resets,
or writes another worktree. Existing archive reuse is content-validated
against the fixed revision. The overlay retains the original test assertions,
records ordered checkpoints in JSONL, and uses only bounded deferred cleanup
to capture the room outcome if an original assertion aborts first.

The raw PR438 observation is preserved under `ci/`. The primary's reported
10/10 rerun is kept separately in `primary-rerun.json` and is explicitly
`REPORTED_UNDER_PAYLOAD` when its command/output transcript is unavailable.

Run order:

1. `capture_ci.py` captures the complete failed job log and metadata.
2. `run.py --matrix matrix.json --aggregate-timeout 900` executes the frozen
   comparison.
3. `verify.py` runs the fail-closed provenance, matrix, overlay, checkpoint,
   cleanup, classification, and causal-map checks.
4. Focused accumulated lifecycle normal/race checks are run from the clean
   candidate source before the evidence-only commit and handoff.

The first four executor attempts are retained as preflight history in
`provenance.json`; they were rejected as invalid setup/overlay executions
(toolchain selection, cache directory creation, module working directory, and
overlay typing) and are not behavioral trials. The final canonical run is
`20260911T143300Z-review52-repair-v2`: 22/22 cells passed on both revisions
in 69.655 seconds, within the 900-second aggregate bound; all six negative
controls were rejected, normal parent-exit cleanup was proven, and the
run-local cache was removed after its retained reports were written. Focused
normal and race regressions also pass from `3af52f8b`. The repaired runner uses
only the declared environment allowlist and its classification controls reject
a label when both explicit orders fail. `storage.json` records source staging,
fixture/report, scratch/cache, free-space before/after, archive reuse identity,
and cleanup values for the canonical run.

The final fail-closed integrity probes reject deleted cleanup fields, survivor
claims, tampered raw selection counts, duplicate or transformed checkpoints,
cross-run trace substitution, tampered CI metadata, selected behavior failures
hidden as selection errors, and mismatched retained archives. `verify.py --mode
all` passes against the final run.

During repair, a pre-final local diagnostic attempt observed one planning-main
`gomaxprocs-4` assertion failure with a peer-cancel snapshot at
`browser_parity_test.go:224`. The fresh canonical run after the cleanup and
verification repairs did not reproduce it. This is disclosed as a bounded
local observation, not as a hosted-CI flake or a causal attribution; the
classification remains `NON_REPRODUCED` for the declared final matrix.

This slice does not repair production code and does not claim PASS, vertical
acceptance, project completion, physical/acoustic proof, Realtime use, or a
CI/review/merge result.
