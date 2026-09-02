# Audio-device server integration testing

The loopback audio-device server makes the production `agent` process
testable without installing a platform loopback driver. The agent connects as
a normal device-registry client; provider transport, resampling, playback
queueing, callback accounting, server VAD, and OpenAI truncation all remain on
their production paths.

Build both executables:

```bash
make build
```

Start a deterministic 16 kHz device server:

```bash
agent-cli/bin/audio-device-server --listen 127.0.0.1:19090 --sample-rate 16000
```

The server prints one JSON ready record naming its endpoint and default
devices. By default it advances capture and render callbacks in real time, so
the default input and output behave immediately without another controller.
Use the endpoint with either a replay or a live provider:

```bash
agent-cli/bin/agent session \
  --replay capture.session.json \
  --audio-device-server 127.0.0.1:19090 \
  --audio-out-device=

agent-cli/bin/agent session \
  --provider openai \
  --audio-device-server 127.0.0.1:19090
```

An omitted input/output selector uses the server's directional default when
that side is enabled by the session. Explicit IDs such as
`simulated-duplex:input` and `simulated-duplex:output` continue to work through
the ordinary flags.

For deterministic tests, start the server with `--manual-clock`. Injected microphone PCM is not read
until capture callbacks advance, and queued speaker PCM is not considered
heard until render callbacks advance. The Go harness helpers are:

- `audio.InjectRemoteDeviceServerCapture`
- `audio.AdvanceRemoteDeviceServer`
- `audio.ReadRemoteDeviceServerSnapshot`

This makes assertions use rendered samples rather than sleeps or received
network bytes. The server accepts only loopback endpoints; authentication is
not part of the local integration contract.

## Scenario names

Scenarios describe behavior rather than capture sequence numbers. Current
examples are:

- `openai-output-lossless-24k-to-16k`
- `openai-server-vad-barge-in-before-first-callback`
- `openai-server-vad-barge-in-after-device-callback`
- `openai-server-vad-late-delta-discard`

Historical capture names such as `eac10.json` belong only in provenance
metadata. New test names and fixtures use semantic scenario names.

Run the required process-boundary replay locally with:

```bash
make test-audio-device-server-integration
```

The ordinary `make test-integration` and `make test-regressions` targets also
select this replay, and CI exposes it as a named integration step.

## Live WebMCP cube exercise

The opt-in Cubecade exercise starts the shipped `agent` and
`audio-device-server` binaries, a pinned Chrome on the real Cubecade site, and
one OpenAI Realtime session. Its temporary workspace supplies an `AGENTS.md`
that translates cube notation into color language and prohibits facelet
dumps. The prompt makes the model create a known unsolved position, inspect
and describe it, then restore and verify the solved board.

Run the billed test on the qualified Darwin arm64 host with:

```bash
cd agent-cli
WEBMCP_CUBECADE_AUDIO_DEVICE_LIVE=1 \
OPENAI_API_KEY_FILE=/absolute/path/to/key \
go test -tags=live ./internal/webmcp/chrome \
  -run '^TestPinnedChromeCubecadeAgentUsesAudioDeviceServer$' -count=1 -v
```

`OPENAI_API_KEY` may be used instead. This test is deliberately outside PR CI:
it contacts a mutable external page and incurs provider usage. It passes only
when the model uses the page's discovered tool references for the exact move
and inverse sequences, reads state at least three times, leaves the independent
DOM oracle solved, describes colors and alignment without raw notation, and
renders drained, lossless 16 kHz PCM through the server.
