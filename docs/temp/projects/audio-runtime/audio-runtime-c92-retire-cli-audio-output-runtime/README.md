# C92 audio output runtime retirement

This task moves assistant-audio sink selection, PCM/WAV framing, loudness,
observation, negotiated device-consumption output, and cancellation-tail
forwarding into `go-agent-runtime/services/audiooutput`.

The public package is host-neutral. Its private implementation is assembled by
the dedicated `services/audiooutput/wire` package. The CLI retains only the
three compatibility entry points, runtime-plan adapter, device observer
bridge, and optional-session capability bridge.

Focused evidence for this candidate:

- `go test ./go-agent-runtime/services/audiooutput/...`
- `go test ./agent-cli/internal/services/internal/agentruntime`

The current-main merge and script CI gate remain handoff concerns; this note
does not claim CI completion.
