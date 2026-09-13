# C139 disjoint recovery checkpoint

The admitted project is `audio-runtime` / `audio-runtime-v1`. Admission is
confirmed by `factory/scripts/project-control.py verify-work --type task`, and
the isolated branch matches `prd.json.branchName`. The branch starts at
accepted `origin/main` `915ed982d23f2e549e529ff43c4f370b4b51e394` and preserves
startup integration `8bdafc7f947a3a2c9856220abdc539437035bd21`.

The preserved C91 implementation was transplanted without touching its branch,
PR 481, predecessor evidence, the host checkout, or peer worktrees. The C139
candidate keeps the public captureclaim contract, private implementation,
dedicated Wire construction, GOWORK=off consumer, durable no-overwrite
publication, redacted holder metadata, inode ownership, typed errors, cleanup,
and bounded idempotent release. The CLI adapter is 77 lines versus the 317-line
baseline, retiring 240 production lines.

Focused normal/race captureclaim and CLI tests, the standalone consumer, all
five fail-closed verifier modes, and the accumulated normal/coverage/race
session regression matrix pass. Pinned vet, golangci-lint 2.9.0, staticcheck
2026.1, and stable Wire generation pass.

Architecture reproduces exactly the four predecessor findings: one generated
captureclaim Wire registration and three stale `acquireSessionRecordingClaim`
baseline entries. These shared files remain read-only while C110, C112, and
C119 hold their active leases. Coverage registration also reports the unrelated
current-main `session/internal/live/causal` missing manifest; C139 does not own
that path. C138 is read and accepted only for its C127 vertical evidence.

Next action: push/open the C139 recovery PR, retain ownership, then after the
shared guarded merges and an explicit exact ownership grant, integrate the
then-current accepted main and apply only the demonstrated four shared-path
repairs before the one changed-head SCRIPT CI submission.
