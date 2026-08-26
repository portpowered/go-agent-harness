# s2s-e2e-conversation-observability — durable artifact proof is ACHIEVED

Status: **achieved and formally proven in-repo** (2026-08-25).

The lane's milestone — logs and recordings written by the CLI session command
alone, not test instrumentation, prove that a multi-turn spoken conversation
happened end to end over the replay transport — is proven by a hermetic,
deterministic, CI-gating test with no network and no credentials.

## What is proven

`TestSessionCommandConversationObservabilityProvesConversationFromArtifactsOnly`
(`agent-cli/test/integration/s2s_e2e_conversation_observability_test.go`)
drives a four-turn spoken conversation through the shipped CLI session surface
exactly like the depth-4 proof — one `agent session --replay <slice>
--audio-in <corpus wav>` invocation per turn — but additionally passes
`--record-dir`, so every turn leaves a complete recording directory under a
shared root (`turn-01/` … `turn-04/`). Each directory contains the standard
recording bundle:

- `client.transcript.jsonl` / `agent.transcript.jsonl` — both-side frame
  transcripts,
- `audio/in-NNN.pcm` — the exact user utterance audio that reached the wire,
- `audio/out-NNN.pcm` — the assistant reply audio the client received,
- `manifest.json` — versioned metadata plus SHA-256 of every artifact,
- `session-log.jsonl` — NEW: a machine-readable per-turn conversation log with
  stable JSONL field order:

```json
{"turn_index":1,"input":{"text":"","audio_bytes":23040,"committed":true,"audio_segments":["audio/in-000.pcm", "…", "audio/in-023.pcm"]},"response":{"text":"ZEPHYR noted. I will remember it.","complete":true,"audio_bytes":23040,"audio_segments":["audio/out-000.pcm"]}}
```

The log is derived by the recording finalizer from observed stream traffic
(input text items, end-of-turn commit markers, response text/transcript
deltas and done events, per-turn audio segments), so it exists for any caller
of `--record-dir`, live or replayed. Callers who do not request artifacts are
unaffected.

After all four turns complete, the test re-reads ONLY those on-disk
artifacts — no live event stream, no command stdout participates in any
assertion — and proves per turn, in directory order:

1. **Ordered turns.** The artifact set holds one recording directory per
   driven turn; each single-turn session log carries exactly one entry whose
   reply matches the expected scripted response for that position in the
   conversation ("ZEPHYR noted. I will remember it." → "Sunny and mild
   today." → "The word was ZEPHYR." → "Backwards it is RYHPEZ."), marked
   complete.
2. **Specific utterances went in.** The recorded input segments concatenated
   are byte-identical to the committed per-turn corpus WAV PCM
   (`multiturn_turn1..4.wav`): the artifacts pin which spoken utterance each
   ordered turn carried, together with its end-of-turn commit marker.
3. **Audible replies came out.** The recorded reply audio has RMS energy
   strictly above the repo's documented silence threshold (500.0 on the PCM16
   linear scale, per the depth-3 proof; voiced corpus speech measures ~2000).
4. **Integrity.** Every manifest-listed artifact re-hashes to its recorded
   SHA-256, so the bundles describe exactly the bytes being judged.

Because each bundle's log records one session-local turn, the conversation
order is carried by the ordered `turn-NN` directory sequence plus the pinned
utterance bytes inside each directory; multi-turn sessions would accumulate
`turn_index` 1..N within a single log.

## Negative control proves non-vacuous coverage

`TestSessionCommandConversationObservabilityNegativeControlFailsTruncatedArtifacts`
copies the positive artifact set, removes `turn-04/session-log.jsonl`
(truncated evidence) and replaces `turn-02/audio/out-000.pcm` with digital
silence of identical length (redacted reply audio), then runs the IDENTICAL
assertion. It fails, naming what is missing — e.g. `turn-02: recorded reply
RMS = 0.0, want > 500.0 (silence threshold)` and `turn-04: session log
unreadable`.

## How to reproduce locally

```sh
cd agent-cli
go test ./test/integration/ -run TestSessionCommandConversationObservability -v -count=1
```

No network, no credentials. The fixtures are synthesized at runtime from the
committed corpus WAVs following the fixture-hygiene policy (raw frame bytes
are injected at load time, never committed).

## Historical context

Depth-3 (#157) proved the audio roundtrip and depth-4 (#158) proved cross-turn
context carry, but both asserted against live in-process output streams; had
all instrumentation been removed, no durable on-disk evidence set existed.
The proof of record for this lane is the hermetic test pair named above.
