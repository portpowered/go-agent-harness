# Live mixed-modality confirmation

The opt-in live test exercises the repaired `--image` plus finite
`--audio-in-turn` composition once. It uses `gpt-realtime-2.1-mini`, a
five-word response instruction, a fresh non-default config directory,
`--record-dir` without `--record`, and `--max-duration 60s`.

Provide a Realtime-enabled OpenAI key and explicitly acknowledge the one
billed call:

```bash
export OPENAI_API_KEY=...
export AGENT_HARNESS_LIVE_MIXED_MODALITY=1
export AGENT_HARNESS_LIVE_MIXED_MODALITY_ARTIFACT_DIR=/secure/local/artifacts
go test -tags live -v ./test/integration \
  -run '^TestLiveMixedModalityRecordDirOnlyWithImage$' \
  -count=1
```

The test generates the supplied 64×64 red-square/blue-diagonal PNG and uses
the committed spoken image-description WAV. It validates the finalized
customer-visible recording directory, non-empty input and output audio,
one committed completed response, and grounded red/square/blue/diagonal
terms in both the session transcript and CLI output. Set the artifact
directory only to a private location: retained recordings contain raw
provider media and are not source-controlled or included in PR comments.
