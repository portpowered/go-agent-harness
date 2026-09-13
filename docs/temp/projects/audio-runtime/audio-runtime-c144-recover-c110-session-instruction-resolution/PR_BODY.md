## C144 recovery handoff for the C110 delivery

Logical Work: `audio-runtime-c144-recover-c110-session-instruction-resolution`

Admitted project: `audio-runtime/audio-runtime-v1`

The physical delivery identity remains the preserved C110 branch and PR 497:
`codex/audio-runtime-c110-retire-cli-session-instruction-resolution`. The
current source checkpoint is `3d5b90bc7e6c090c3079bee9edc71ae82391260d`, with
startup ancestor `8bdafc7f947a3a2c9856220abdc539437035bd21` and fetched
`origin/main=4a1c399ccbb3d780be95eb04316e84b8f11a6646`. The local checkpoint is
27 commits ahead of remote head
`372a3e9bc6cbb80e111b6d4d8eed79d405731f42`. The C79 shared-registry lease is
released; only demonstrated sessioninstructions Wire and downward baseline
repairs are retained.

Fresh exact-source evidence:

- `run-20260913T200336Z-67684` rebuilt the credential-free `nomicrophone` YUI
  from `3d5b90bc7`; it is 51,183,106 bytes with SHA-256
  `89220655d1952f8dfe7a355e82d8944238f6f062699b7328f240868350ee7a0b`.
- Instruction/text-seed replay, invalid-before-provider, bounded process
  reaping, the GOWORK=off consumer, mutation guards and retirement/ownership
  verification pass. The adapter is 86 lines and measured retirement is 191
  production lines.
- Focused sessioninstructions normal/race tests, CLI normal tests (207), the
  accumulated normal/coverage/race session regression matrix, coverage
  registration, Wire, architecture-size (204 packages, 1,944 files, 28,797
  functions), vet, pinned staticcheck 2026.1, pinned golangci-lint 2.9.0 and
  diff-check pass.
- One three-repeat CLI race selection had 620 passes and one failure in
  `TestRunRoomWithResultReplaysAdmittedBundleWithoutLiveConfiguration`.
  Five standalone normal repeats passed; the room replay path is outside this
  task's owned diff and remains preserved peer evidence.

The C110 CI rejection and lifecycle findings remain retained in the C110
evidence directory; no assertion, timeout, baseline floor or peer failure was
waived. This changed candidate is ready for the Script CI gate. CI-green,
independent review, guarded merge and post-merge vertical acceptance are not
claimed.
