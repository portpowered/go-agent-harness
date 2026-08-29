# Live mixed-modality confirmation

The opt-in live tests exercise the repaired `--image` plus finite
`--audio-in` composition once and the repaired `--image` plus two-turn
`--audio-in-turn` composition once. Each run uses `gpt-realtime-2.1-mini`, a
five-word response instruction, `--max-duration 60s`, and `--record`.

Provide a Realtime-enabled OpenAI key and explicitly acknowledge the two
billed calls:

```bash
export OPENAI_API_KEY=...
export AGENT_HARNESS_LIVE_MIXED_MODALITY=1
export AGENT_HARNESS_LIVE_MIXED_MODALITY_ARTIFACT_DIR=/secure/local/artifacts
go test -tags live -v ./test/integration \
  -run '^TestLiveMixedModality(FiniteAudioWithImage|ScheduledAudioWithImage)$' \
  -count=1
```

The tests generate the red-square/blue-diagonal PNG and use the committed
spoken image-description WAV. They inspect the retained provider capture for
one image item, non-empty audio appends, one commit and response request for
the finite run, two ordered lifecycles for the scheduled run, completed
responses, and grounded red/square/blue/diagonal terms. Set the artifact
directory only to a private location: retained captures contain raw provider
media and are not source-controlled or included in PR comments.
