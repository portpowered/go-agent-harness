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
