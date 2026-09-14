# C144 recovery adoption evidence

This namespace records the C144 logical Work identity while preserving the
physical C110 delivery branch, worktree, PR 497, predecessor PRD and prior
evidence. The C110 worktree is the sole implementation/delivery surface:

- branch `codex/audio-runtime-c110-retire-cli-session-instruction-resolution`
- local checkpoint `3d5b90bc7e6c090c3079bee9edc71ae82391260d`
- remote PR 497 head before this checkpoint
  `372a3e9bc6cbb80e111b6d4d8eed79d405731f42`
- fetched `origin/main`
  `4a1c399ccbb3d780be95eb04316e84b8f11a6646`
- startup ancestor `8bdafc7f947a3a2c9856220abdc539437035bd21`
- 27 commits ahead of the delivery remote, with the five predecessor evidence
  files refreshed in place

Admission was verified for the sole `audio-runtime/audio-runtime-v1` project
with `factory/scripts/project-control.py verify-work --type task --name
audio-runtime-c144-recover-c110-session-instruction-resolution`. The logical
C144 branch name matches its isolated C144 setup worktree; execution adopts the
preserved C110 physical branch because PR 497 and its source checkpoint are
the delivery identity. The C144 setup worktree was fast-forwarded to the
preserved checkpoint for read-only logical adoption; it was not pushed as a
second delivery branch.

The refreshed source-pinned run is
`run-20260913T200336Z-67684`. Its credential-free `nomicrophone` YUI is
51,183,106 bytes with SHA-256
`89220655d1952f8dfe7a355e82d8944238f6f062699b7328f240868350ee7a0b`.
Instruction/text-seed replay, invalid-before-provider controls, clean process
reaping, the GOWORK=off two-instance consumer, mutation guards, the 86-line
adapter/191-line retirement check, focused normal/race tests, accumulated
normal/coverage/race regressions, coverage registration, Wire, architecture,
vet, pinned staticcheck, pinned lint and diff-check all pass.

The prior CI rejection remains preserved in the C110 namespace. Its five
findings were the four stale C110 baseline entries and the unregistered
sessioninstructions Wire output; C79's guarded merge released those shared
registries, and the current checkpoint contains only the demonstrated repairs.
The peer high-rate failure remains external evidence, not a waiver.

One three-repeat CLI race selection also exposed the existing room replay
failure `TestRunRoomWithResultReplaysAdmittedBundleWithoutLiveConfiguration`
after 620 passing tests. Five standalone normal repeats passed, and no C110
owned room replay path changed, so the result is retained as a peer residual.

This is implementation handoff evidence only. Script CI, independent review,
guarded merge, the immutable post-merge vertical probe and project acceptance
remain open.
