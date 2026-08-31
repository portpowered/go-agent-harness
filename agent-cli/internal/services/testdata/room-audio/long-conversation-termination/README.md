# Long-conversation termination golden

This finalized, privacy-safe synthetic bundle records eight ordered turns
across two participants over 24 seconds. Each participant completes four
turns; the room reaches `max_turns=4` only after the final provider-authored
completion. Both participants therefore end with `reason=ended`, an empty
error, `termination_disposition=completed`, and provider terminal provenance.

The bundle is an offline replay/termination fixture, not an audio-quality
baseline. The clean turn-taking bundles, including the archived
`clean-turn-taking-baseline` control when present, remain reserved for audio
properties and are not used by termination assertions. No provider,
microphone, credential, private recording, or host path is included.

Regenerate it from the repository root with:

```sh
go run ./agent-cli/internal/services/testdata/room-audio/generate.go \
  --shape long-conversation-termination \
  --output agent-cli/internal/services/testdata/room-audio/long-conversation-termination
```

Verify admission, artifact hashes, sidecars, timeline outcomes, and replay
stability offline with:

```sh
go test ./agent-cli/internal/services \
  -run TestLongConversationTerminationGoldenReplaysCleanly -count=1
```
