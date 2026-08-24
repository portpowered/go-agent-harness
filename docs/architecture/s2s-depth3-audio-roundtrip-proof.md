# s2s depth-3 milestone — audio-in to audio-out roundtrip is ACHIEVED

Status: **achieved and formally proven in-repo** (2026-08-24).

The depth-3 milestone of the s2s program — a full audio roundtrip through the
shipped CLI: microphone/file audio in, spoken model audio out — is no longer
limited to informal live verification. It is proven by a hermetic,
deterministic, CI-gating test with no network and no credentials.

## What is proven

`TestSessionCommandAudioRoundtripRecordsNonSilentReply`
(`agent-cli/internal/services/session_audio_roundtrip_proof_test.go`) drives the
real `agent session --audio-in <wav> --audio-out <path>` command surface over
the record/replay WebSocket transport using real committed WAV fixtures from
`go-agent-loop/testdata/audio`, and asserts:

1. **CLI roundtrip.** The command executes end to end: streamed input audio,
   `input_audio_buffer.commit`, `response.create`, transcript deltas, and a
   terminal `response.done` carrying output audio bytes.
2. **Non-silent recorded reply.** The written output WAV is parsed; its RMS
   energy must exceed the documented threshold (500 on the PCM16 linear scale;
   voiced speech windows in the corpus measure ~2000, digital silence
   measures 0).
3. **Plausible duration bounds.** The recorded duration stays within bounds
   derived from the scripted reply carried by the replay fixture.
4. **Byte-accurate full-stream delivery.** The record/replay transport
   validates every outbound frame against the fixture payload-for-payload, so
   any mid-stream truncation or byte drift fails the run. This guards the
   #156 regression class (the client once silently truncated at ~35% of the
   file before reaching EOF; fixed by pacing file-backed streaming at
   real-time, commit b2ac97d).

Two negative controls prove the assertions discriminate rather than pass
vacuously:

- `TestSessionCommandAudioRoundtripSilentInputFailsAssertions`: a silent
  corpus WAV (`silence_16k.wav`) through the identical CLI surface produces a
  recording that the positive RMS/duration assertion rejects with observed vs
  expected values.
- `TestSessionCommandAudioRoundtripTruncatedInputFailsReplay`: an input stream
  that does not match the fixture's expected frames diverges the replay and
  errors the command.

## Historical context

An earlier live verification by the operator on 2026-08-24 (commit b2ac97d)
demonstrated the milestone against the live OpenAI Realtime API. That live
call was informal evidence only — it is never CI-gating. The proof of record
is the hermetic test named above.
