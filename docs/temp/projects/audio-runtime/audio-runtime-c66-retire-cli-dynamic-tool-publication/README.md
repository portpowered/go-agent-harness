# C66 dynamic tool publication evidence

This directory contains the C66-owned verification controls and the separate
`GOWORK=off` consumer for the public `toolpublication` contract.

The frozen production baseline is `d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`
with 593 physical lines in the legacy CLI controller. The adapter is 232 lines
at the current checkpoint, a net retirement of 361 production lines; the two
existing session callers remain byte-identical to that baseline.

The focused service, adapter, consumer, replay-control and bounded-cleanup
commands are intentionally credential-free. The managed-browser case is
classified as a hermetic WebMCP adapter regression, not live customer or
project acceptance; the audio case is offline replay evidence, not physical or
acoustic proof.

C66 also converts its two public failure identities to comparable typed
constants, preserving `errors.Is` identity without mutable package state. The
remaining architecture gate output is exactly 9 shared findings: 8
downward/stale entries in `docs/architecture/architecture-size-baseline.json`
owned by C61, plus the generated Wire path absent from the shared
`scripts/wire-packages.txt` registry owned by C57. C66 does not edit either
shared file; no architecture-policy exception is needed for the sentinels.
