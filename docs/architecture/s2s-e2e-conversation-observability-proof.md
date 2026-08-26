# s2s-e2e-conversation-observability — durable artifact proof is ACHIEVED

Status: **achieved and formally proven in-repo** (2026-08-26).

The lane's milestone — logs and recordings written by the CLI session command
alone, not test instrumentation, prove that a multi-turn spoken conversation
happened end to end over the replay transport — is proven by a hermetic,
deterministic, CI-gating test with no network and no credentials.

## What is proven

`TestSessionCommandConversationObservabilityProvesConversationFromArtifactsOnly`
(`agent-cli/test/integration/s2s_e2e_conversation_observability_test.go`)
drives a four-turn spoken conversation through one invocation of the shipped
CLI session surface with four repeatable `--audio-in-turn <corpus wav>` flags
and one `--record-dir`. The persistent replay session therefore leaves one
recording bundle whose `session-log.jsonl` contains all four turns:

- `client.transcript.jsonl` / `agent.transcript.jsonl` — both-side frame
  transcripts,
- `audio/in-NNN.pcm` — the exact user utterance audio that reached the wire,
- `audio/out-NNN.pcm` — the assistant reply audio the client received,
- `manifest.json` — versioned metadata plus SHA-256 of every artifact,
- `session-log.jsonl` — an ordered machine-readable conversation log with
  stable JSONL field order:

```json
{"turn_index":1,"input":{"text":"Remember the word ZEPHYR.","audio_bytes":23040,"committed":true,"audio_segments":["audio/in-000.pcm"]},"response":{"text":"ZEPHYR noted. I will remember it.","complete":true,"audio_bytes":23040,"audio_segments":["audio/out-000.pcm"]}}
```

The log is derived by the recording finalizer from observed stream traffic
(provider input-ASR transcripts, end-of-turn commit markers, response
text/transcript deltas and done events, and per-turn audio segments), so it
exists for any caller of `--record-dir`, live or replayed. Callers who do not
request artifacts are unaffected.

After the one session completes, the test re-reads ONLY that on-disk artifact
directory — no live event stream and no command stdout participates in any
assertion — and proves:

1. **Ordered turns and text.** `session-log.jsonl` contains four entries with
   `turn_index` 1 through 4. Each entry contains the provider's complete input
   transcript and the full scripted response in conversation order:
   "ZEPHYR noted. I will remember it." → "Sunny and mild today." → "The word
   was ZEPHYR." → "Backwards it is RYHPEZ.", with every response marked
   complete.
2. **Specific utterances went in.** The log's input segment lists point to
   recorded PCM that is byte-identical to the committed per-turn corpus WAV
   (`multiturn_turn1..4.wav`), and every turn has an end-of-turn commit marker.
3. **Audible replies came out.** The log's output segment lists point to
   recorded reply audio whose RMS energy is strictly above the repo's
   documented silence threshold (500.0 on the PCM16 linear scale, per the
   depth-3 proof; voiced corpus speech measures ~2000).
4. **Integrity.** Every manifest-listed artifact re-hashes to its recorded
   SHA-256, so the bundle describes exactly the bytes being judged.

## Negative control proves non-vacuous coverage

`TestSessionCommandConversationObservabilityNegativeControlFailsTruncatedArtifacts`
copies the positive artifact set, truncates `session-log.jsonl` to three
entries, and replaces the second reply segment (`audio/out-001.pcm`) with
digital silence. It then runs the IDENTICAL assertion. It fails while naming
both missing evidence classes — the truncated session log and, for turn 2,
`recorded reply RMS = 0.0, want > 500.0 (silence threshold)`.

## How to reproduce locally

```sh
cd agent-cli
go test ./test/integration/ -run TestSessionCommandConversationObservability -v -count=1
```

No network, no credentials. The replay capture is derived at test setup from
the committed corpus fixture; raw input and output audio bytes are injected
only in the temporary fixture and recording directory, never committed.
