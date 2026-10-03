# C143 recovery of the preserved C109 browser-retirement delivery

This namespace records the C143 recovery evidence for the sole admitted
`audio-runtime` project at contract `audio-runtime-v1`. C143 is the logical
Work identity. Execution adopts the existing C109 branch, worktree, PR 495,
commits and clean checkpoint; it does not create, rename, reset, rebase or
overwrite that delivery.

## Adoption and preservation

- Logical task branch/worktree: `codex/audio-runtime-c143-recover-c109-browser-retirement-integration`, the isolated C143 shell matching `prd.json`.
- Adopted delivery: `codex/audio-runtime-c109-characterize-browser-retirement-integration` at `b4e9042f8bb0c2a12274d72b7fd8546753ef20b6`, tree `c4722ad2abaeca307e06534bc10447f62fe9c214`, PR 495.
- Accepted main is `d4766c3dbbf2c198142047ead4449d58dd47d485`; startup integration is `8bdafc7f947a3a2c9856220abdc539437035bd21`; a fresh fetch resolved review-time `origin/main` to `4a1c399ccbb3d780be95eb04316e84b8f11a6646`, already integrated in the adopted checkpoint.
- C61 remains `8e8177c031a7b3b9322d712af19970e13fa7a1bc` with tree `8d9ce4cb0b0654ac021b42651d9f69f6614922a2`; C83 remains `22cc6769aaf06d1e2c1275b064cc7ec29de3e371` with tree `1bebaaa7ed3e47b4db24b82571000cd8e08b3048`. Both predecessor worktrees were clean and byte-identical before and after the checks.
- Root task-file hashes were retained for both the logical C143 shell and adopted C109 worktree. `prd.json`, `progress.txt`, the C109 historical namespace, production/test source, shared registries and peer worktrees were not modified.

The machine-readable identity and preservation ledger is
`recovery-report.json`; `evidence-manifest.json` binds every generated output
hash and retained companion. The large source-derived analyzer report/ledger
pair was validated before checkpointing and is represented by its exact hashes
in the C143 manifest; the complete historical C109 pair remains preserved, so
this recovery does not duplicate the 57 MiB analyzer bundle. No historical
C109 report was replaced.

## Fresh bounded evidence

The required `main -> C61 -> C83` and reverse control `main -> C83 -> C61`
rehearsals each used a separate disposable root, converged to synthetic tree
`4d8e93ae0e74db403fdb02c70db9cf849c590156`, preserved exact refs and
predecessor worktrees, and passed cleanup. Complete source-derived analyzer
reports were validated before their exact hashes were retained in the
compact C143 manifest; `test_analyze.py -v` passed 8/8.

`verify.py --mode all` passed all eight checks, deterministic reruns, 19
negative fixtures and the caller-tree mutation control. The explicit
changed-SHA control rejected with `changed C61 SHA`; the incomplete-public-
report control rejected with `public check set is incomplete or does not match
command_set`. The fresh public reports were independently validated against
the verifier's report contract.

The credential-free public matrix passed 18/18 in 255.581 seconds under the
90/300-second bounds. It discovered the browser service normal/race tests,
both GOWORK=off consumers, the nomicrophone cross-module cancellation
coverage, and the accumulated normal/coverage/race session regressions. It
emitted `PROBE_TOOL_MARKER_9182` and `strict replay continuation`, retained
the exact local software effects, scrubbed credentials, preserved the
synthetic tree, and left no process group. The malformed/canceled matrix
passed 3/3 in 35.456 seconds under 60/180 seconds with the same cleanup and
credential controls.

These are software/local-process evidence only. The historical provider-audio
terminal-drain signatures and other inherited CI failures remain preserved
under their exact external owners; C143 did not edit, duplicate, waive or
relabel them. Native hardware/endpoints and physical acoustic proof are out of
scope and are not claimed.

## Handoff state

This is a clean executor checkpoint, not CI, review, merge, vertical runtime
acceptance or project completion. The next action is to commit and push this
C143 evidence on the same adopted C109 branch, update PR 495, and return
`ACCEPTED` to the script-owned current-head CI gate without polling it. Any
exact in-scope CI rejection remains with this task for causal repair and a
changed-head resubmission; do not self-review.
