# C143 fresh recovery revalidation — 2026-09-13

Admission was verified with the required task command and returned admitted
for `audio-runtime` / `audio-runtime-v1`. The C143 `prd.json.branchName` is
`codex/audio-runtime-c143-recover-c109-browser-retirement-integration` and
matches the isolated C143 worktree. The physical delivery was preserved and
adopted at the existing C109 worktree/branch and checkpoint
`b4e9042f8bb0c2a12274d72b7fd8546753ef20b6`; no branch rename, reset, rebase,
worktree recreation or host-checkout merge was used.

`git fetch origin main` resolved `origin/main` to
`4a1c399ccbb3d780be95eb04316e84b8f11a6646`, already an ancestor of the
adopted checkpoint. Startup `8bdafc7f`, baseline `3194edd9`, accepted main
`d4766c3d`, C61 `8e8177c0`, and C83 `22cc6769` remain ancestral or pinned as
required. PR 495's preserved planning run `34772784959` was green at the old
head `b4e9042f`; it does not certify this C143 descendant.

The fresh required and reverse analyzer runs retained compact companions under
`runs/final/required` and `runs/final/control`; complete source-derived
report/ledger outputs were validated before checkpointing and are bound by
their exact hashes in `evidence-manifest.json` without duplicating the
historical 57 MiB analyzer bundle. They both passed, used separate disposable
roots, recorded conflict/API ownership and abort/cleanup evidence, retained
clean predecessor worktrees, and converged to synthetic tree
`4d8e93ae0e74db403fdb02c70db9cf849c590156`. `verify.py --mode all` passed with
deterministic output, 19 fail-closed negatives and caller-tree mutation
rejection. `test_analyze.py -v` passed 8/8 in 10.287 seconds.

Fresh public reports are in `runs/final/public`. The credential-free
browser/audio/tool case passed 18/18 in 255.581 seconds with child/aggregate
limits 90/300; the malformed-or-canceled case passed 3/3 in 35.456 seconds
with limits 60/180. Both reports are bound to the exact synthetic tree,
credential-free, effect-bearing, and cleanly reaped. Fresh report validation
passed for both cases. The positive workflow retained `PROBE_TOOL_MARKER_9182`
and `strict replay continuation`; the negative workflow retained its expected
negative classification.

All prior review repairs and historical CI rejection records remain in the
C109 namespace. The current C143 evidence does not alter the known external
C79/provider-audio terminal-drain ownership or any inherited CI signature.
C61/C83, CI, independent review, guarded merge, the C143 vertical probe and
all broad project gates remain open.

The exact file hashes, preserved identities and run bindings are in
`recovery-report.json`. This checkpoint is executor evidence only. After the
checkpoint commit, push the adopted C109 branch, update PR 495, and submit the
changed head to script CI without polling.
