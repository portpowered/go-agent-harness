# C87 implementation checkpoint

Admission was verified against the sole admitted `audio-runtime` project and
the exact `audio-runtime-v1` contract. The planning-main copy of the retired
CLI file is 243 physical production lines; this candidate keeps the CLI file
as a 40-line deprecated alias/delegation adapter.

The public `sessionturns` package contains the stable input, event, result,
error, and service contracts. The private `internal/service` package owns
state, transport protocol, copied snapshots, serialized event publication,
and bounded close. `sessionturns/wire` is the sole constructor edge.

Focused evidence currently passes:

- five-turn reuse, exact event ordering, copied text/audio input and response
  snapshots, invalid transition preservation, nonterminal error skipping,
  terminal error identity, cancellation, audio commit rejection, and single
  close behavior;
- normal and race tests for the extracted service, and the deprecated CLI
  compatibility adapter;
- the separate external consumer with `GOWORK=off`, including two independent
  Wire-built services;
- the credential-free bounded runner with 60-second child and 300-second
  aggregate limits.

The candidate has not claimed script CI, independent review, guarded merge,
primary vertical acceptance, or project acceptance. The shared Wire registry
and architecture-size baseline remain untouched while their active C79 lease
is unreleased; they are the next integration action after the exact lease
release and accepted-main refresh.

## Static-gate repair checkpoint

CI run `34686814971` evaluated source head `8fb7536f06f238847d6ea863a60548788e631947`.
Unit, race, WebMCP Chrome, macOS audio release, and Windows portable checks
passed. Static reported two C87-owned `contextcheck` findings in the deprecated
CLI compatibility fixture, plus one unregistered
`go-agent-runtime/services/sessionturns/wire/wire_gen.go` and twelve stale
downward baseline entries for the retired CLI symbols. The latter thirteen
findings are shared C79 ownership and remain deferred.

Commit `243865ef280ccfd416fd9c6fb9c7dec378b7949c` repairs the owned findings by
propagating the inherited send context, handling every typed send outcome,
preserving the close error when a connection loses a close race, and checking
test helper results and type assertions. On that source revision, pinned
`make lint` reports 0 issues in all 15 modules, `make staticcheck` passes,
`make coverage-registration` checks 179 packages, service normal/race tests
pass 48/48, CLI compatibility normal/race passes 6/2, the external
`GOWORK=off` consumer passes, all three verifier modes pass, the credential-free
consumer program/tests pass within 60-second child and 300-second aggregate
limits, and `COUNT=1 scripts/test-session-ci-regressions.sh all` passes normal,
coverage, and race modes. `make architecture-size-check` still reports exactly
the same 13 shared Wire/baseline findings; no shared file was changed.

This is executor evidence only. The repaired head is not yet submitted to the
script CI gate because C79's reviewed guarded merge has not released the shared
registry and architecture-baseline lease. After that release, fetch accepted
main, apply only the C87 Wire registration and demonstrated downward/deletion
baseline edits, rerun the bounded gates, and submit the changed same-task PR.
