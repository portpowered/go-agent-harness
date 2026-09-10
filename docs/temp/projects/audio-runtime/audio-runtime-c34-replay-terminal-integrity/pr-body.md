## Summary

- Enforce one leading `recording_started` and one final clean `recording_closed` event.
- Reject missing, duplicate, non-leading, unclean, and post-close lifecycle events before exposing a replay.
- Preserve the public `recording.OpenReplay`, `Replay.Next`, and `Replay.Clock` APIs and existing timestamp/PCM/hash behavior.

## Repair of the rejected candidate

The prior head `ee15be2f0e4093e12c06c32df4e7a8ed4219861d` was rejected by CI (static) for four OpenReplay architecture-baseline drifts and four pinned-lint `goconst` findings. The exact raw run and job log are preserved in the C34 evidence directory. The repair keeps the admitted manifest and baseline unchanged; `make architecture-size-check` and pinned `make lint` now pass.

## Validation

- `go test -C go-audio ./pkg/recording` — PASS
- `go test -C go-audio -race ./pkg/recording` — PASS
- `go vet -C go-audio ./pkg/recording` — PASS
- Public consumer verification — PASS: two valid sessions accepted; eight malformed lifecycle fixtures returned `ErrIncomplete` with `replay_exposed=false`.
- Existing software replay regression — PASS: `Replay verified: 15 wire events, 0 tool calls`; rendered PCM is 3,360 bytes with SHA-256 `302e7421a29a4868a0a1a2f1ca2e8432c9015a6475412ec63fe2b15414f469ff`.

Evidence reports record the exact source revision, build controls, fixture hashes, command output, and preserved YUI/bundle hashes. This is software replay evidence only; it does not claim physical-device or acoustic proof.

CI remains script-owned for this candidate; no CI result is claimed here.
