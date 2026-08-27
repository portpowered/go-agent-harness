# s2s vertical v2b — long-utterance audio-in streams many appends and commits exactly once

Status: **proven in-repo** (2026-08-25) by a hermetic, deterministic,
CI-gating integration lane. No network access, no credentials, no audio
hardware.

## What is proven

A single utterance longer than ten seconds, played into the shipped agent CLI
through `--audio-in`, is streamed over the Realtime wire as **many**
`input_audio_buffer.append` events and ends the turn with **exactly one**
`input_audio_buffer.commit`, **exactly one** `response.create`, and **exactly
one** completed server turn — never one commit per chunk, never a truncated
stream.

For the committed corpus fixture `utt_long_16k.wav` this means:

```text
appends = ceil(228750 samples / 480 samples-per-frame) = 477
commits = 1
response.create = 1
completed turns (terminal response.done) = 1
```

A regression that commits per chunk would split turns (duplicate billing,
broken barge-in semantics, garbled transcripts) while internal service-level
tests stay green. This lane makes that regression fail CI through the public
command surface.

## The exact command

The proof enters only through the real shipped CLI executed as a subprocess
(the binary built once per package run from `./cmd/agent`):

```bash
agent session \
  --config-dir <tempdir> \
  --max-duration 30s \
  --replay <runtime-generated .session.json fixture> \
  --audio-in go-agent-loop/testdata/audio/utt_long_16k.wav \
  --audio-out <tempdir>/response.wav
```

No internal agent-loop or session-service function call establishes the proof.
Tests:
`TestS2SV2BAudioInLongCLIStaysOneTurn` (positive) and
`TestS2SV2BPerChunkCommitFixtureFailsIdenticalInvocation` (negative control)
in `agent-cli/test/integration/s2s_v2b_audio_in_long_test.go`.

## Fixture provenance

- **Audio:** reused from the committed speech corpus,
  `go-agent-loop/testdata/audio/utt_long_16k.wav` (mono PCM16, 16 kHz,
  228750 samples ≈ 14.30 s). No new WAV or binary fixture blob is committed by
  this lane.
- **Replay capture:** generated at runtime into `t.TempDir()` by the test.
  The handshake, server-response, and `session.closed reason=fixture_complete`
  records come from the committed OpenAI smoke fixture
  (`openai_realtime_smoke.session.json`); its client text-input record is
  replaced by 477 `input_audio_buffer.append` records carrying byte-exact
  PCM16LE frames plus one `input_audio_buffer.commit` and one
  `response.create`.

## Append-count derivation

The test reads the WAV header with the gateway's `wavio` decoder and derives
the expected append count as `ceil(samples / 480)` — 480 samples per 30 ms
frame at 16 kHz, mirroring the finite file source's frame contract. The last
frame is zero-padded when the sample count is not an exact multiple. The
derivation is load-bearing twice: the positive assertion requires the replayed
append count to equal the derived count, and both tests fail outright if the
derived count is not greater than one (a short-sample corpus would make the
"many appends" claim vacuous).

## Why commit cardinality is observable

The hermetic record/replay transport validates every outbound
client-to-server event **byte-for-byte and in strict order** against the
capture (`go-llm-gateway/pkg/testing` replay WebSocket dialer). Any extra
event, missing event, truncation, or payload divergence terminates replay
with a typed mismatch that names the expected and actual events. Therefore a
clean run — exit 0, the scripted reply transcript printed exactly once, the
`[session closed: fixture_complete]` terminal marker on stdout, and a
non-silent recorded response WAV — proves the exact wire shape including the
N-appends : 1-commit : 1-response.create : 1-turn invariant and emitted audio
delivery. Nothing else could pass the ordered validation.

## Negative control proves non-vacuous coverage

`TestS2SV2BPerChunkCommitFixtureFailsIdenticalInvocation` replays the same
utterance against a companion capture produced by the same builder, differing
from the positive capture **only** by an inserted
`input_audio_buffer.commit` directly after every append (a structural guard
pins this: every positive record unchanged and in order, one inserted bare
commit per append, nothing else added, removed, or reordered).

The identical CLI invocation must fail, and it does, fast — the divergence
surfaces on the second outbound frame:

```text
Error: replay session capture <fixture>: replay mismatch: expected outbound
payload for input_audio_buffer.commit at sequence 4, actual
input_audio_buffer.append
```

The suite passes only by asserting that rejection; substituting the negative
fixture into the positive case would fail the positive assertions.

## Scope boundary

This is hermetic T1 CLI coverage only, exercised over the record/replay
transport. It claims **no live-provider coverage** and deliberately does not
duplicate sibling verticals: v1 text-in/audio-out, v2a basic audio-in,
v2c silence/noise audio-in, v2d multi-utterance segmentation, v2e
truncated-source buffer disposition, or v6a auth-error behavior.
