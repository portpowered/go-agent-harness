# C66 dynamic tool publication evidence

This directory contains the C66-owned verification controls and the separate
`GOWORK=off` consumer for the public `toolpublication` contract.

The frozen production baseline is `d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`
with 593 physical lines in the legacy CLI controller. The adapter is 218 lines
at the current checkpoint, a net retirement of 375 production lines; the two
existing session callers remain byte-identical to that baseline.

The focused service, adapter, consumer, replay-control and bounded-cleanup
commands are intentionally credential-free. The managed-browser case is
classified as a hermetic WebMCP adapter regression, not live customer or
project acceptance; the audio case is offline replay evidence, not physical or
acoustic proof.

The disjoint candidate currently records two shared-gate dependencies. The
architecture gate retains the exact downward/stale entries in the shared
`docs/architecture/architecture-size-baseline.json` owned by C61, and Wire
reports the new generated path as absent from the shared
`scripts/wire-packages.txt` registry owned by C57. The candidate does not edit
either file or add the required architecture-policy exceptions for the two
public sentinel errors while those shared ownership decisions are unresolved.
