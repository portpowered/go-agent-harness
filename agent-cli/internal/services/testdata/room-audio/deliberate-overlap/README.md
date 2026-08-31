# Deliberate overlap room audio fixture

This bundle is an eight-second, privacy-safe synthetic overlap case. Agent A
and agent B speak concurrently from 1.000 s through 6.500 s. Each participant
has an independent sent stream, and the provider-bound `received.pcm` stream
contains the peer delayed by 20 ms. The room mix and room timeline use the
same 1000 Hz absolute sample clock.

The manifest records the `s2s-room-participants-deaf-while-speaking` received-
audio dependency revision, the room-recording completeness revision, the
identity-preserving transformations, the full suite tolerance profile, and
SHA-256/byte-size metadata for every replay artifact. No provider, microphone,
credential, or private recording is used. Samples come from two fixed seeded
pseudo-speech signals; the two large chunk-boundary jumps are intentionally in
loud windows and are retained as impulse candidates rather than quiet clicks.

Refresh the committed bundle from the repository root with:

```sh
go run ./agent-cli/internal/services/testdata/room-audio/generate.go --shape deliberate-overlap --output agent-cli/internal/services/testdata/room-audio/deliberate-overlap
```

Verify it offline with:

```sh
go test ./agent-cli/internal/services -run 'TestDeliberateOverlapRoomReplay' -count=1
```

Normal tests only read this directory. Refreshing it is intentional and should
include the resulting artifact and manifest hash diff in review.
