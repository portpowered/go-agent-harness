# C52 hermetic room-liveness characterization

This directory is the sole C52 executor lease. It contains an evidence-only
comparison of PR438 (`823bd350fe5d11782c38bda87d7b7bfd7d89d7cd`) and the
planning-time integrated main (`7f73c8b3b4ebc99b55b8bb5e802beff024385407`).
The final tested evidence parent is the clean branch head
`d58e48cc3589703aa62fa120d53407aecd134c7d`, which includes refreshed
`origin/main` (`6b4f32b5940d43192774e86f05e3c7c9293c71e3`) as an ancestor.
The fetched `origin/main` may advance; its exact revision is recorded in
`provenance.json` and does not replace the declared comparison input.

The matrix is frozen in `matrix.json` before `run.py` executes. `run.py` makes
clean gzip-compressed git archives (the compression changes no extracted
source bytes), applies only the generated scratch overlay, runs one
bounded first-result-per-cell matrix, and removes only its known scratch trees
after child groups are reaped. It never checks out, resets, or writes another
worktree. The overlay retains the original test assertions and records
ordered checkpoints in JSONL.

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
overlay typing) and are not behavioral trials. The earlier archive-backed
run remains retained, and the final integrated archive-backed run is
`20260911T-6b4f32b5-hermetic`, with 22/22 cells passing on both revisions,
all five negative controls rejected, normal parent-exit cleanup proven, and
all generated caches removed after their retained reports were verified.
The repaired runner uses only the declared environment allowlist and its
classification controls reject a label when both explicit orders fail.

This slice does not repair production code and does not claim PASS, vertical
acceptance, project completion, physical/acoustic proof, Realtime use, or a
CI/review/merge result.
