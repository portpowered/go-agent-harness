# C81 implementation evidence

The CLI room-audio bundle decoder is now a host-neutral runtime service at
`go-agent-runtime/services/roomaudio`. The CLI keeps compatibility aliases and
delegates through the generated Wire service; it no longer owns the decoder,
WAV/PCM parsing, delta reconstruction, metadata, annotation, or tolerance
policy implementation.

The retired five CLI implementation files contained 1,825 production lines.
The remaining CLI adapter is 135 lines, a reduction of 1,690 lines. The new
service is split below the 400-line production-file budget and keeps bounded
artifact reads, SHA-256 validation, typed failures, cloned PCM buffers, and
fail-closed identity/timing checks.

Focused evidence:

- `go test ./go-agent-runtime/services/roomaudio/... -count=1`: 33 passed.
- `go test ./agent-cli/internal/services/internal/agentruntime -run 'TestLoadRoomReplayAudioBundle|TestRoomReplay' -count=1`: 19 passed.
- `go test -race ./go-agent-runtime/services/roomaudio/... ./agent-cli/internal/services/internal/agentruntime -run 'TestLoadRoomReplayAudioBundle|TestRoomReplay' -count=1`: 51 passed.
- `go test ./agent-cli/internal/services/internal/agentruntime -count=1`: 1,067 passed.
- `go vet ./go-agent-runtime/services/roomaudio/... ./agent-cli/internal/services/internal/agentruntime`: clean.
- `GOWORK=off go test ./...` in `external-consumer`: passed; the separate module imports only `roomaudio` and `roomaudio/wire` and observes exact samples/order plus typed malformed-delta failure.
- `GOWORK=off go generate ./services/roomaudio/wire` from `go-agent-runtime`: regenerated `wire_gen.go` successfully.

The architecture analyzer reports only shared follow-up work: stale entries
and the lowered agentruntime baseline in the shared architecture baseline,
plus registration of the new generated Wire file in the shared architecture
policy. `make wire-check` likewise reports only the unregistered
`go-agent-runtime/services/roomaudio/wire/wire_gen.go`. Those shared files are
held by predecessor leases and were not edited in this task.

Repair checkpoint after the current-head static rejection:

- Pinned `make lint`, `make staticcheck`, `make fmt`, and targeted `go vet` are clean.
- Focused roomaudio/CLI tests pass: 33 normal roomaudio, 19 focused CLI, 99 roomaudio race, 19 focused CLI race; the isolated `GOWORK=off` consumer passes.
- `make coverage-registration` passes with 179 workspace packages across 6 modules.
- `make coverage-changed COVERAGE_BASE=84c91ee1b41d9ff0ba7e31f321c61f6e34c7a72f` reached the existing WebMCP cross-process test, which failed only under coverage because its child emitted a receipt on stderr; the exact test passes standalone.
- `make architecture-size-check` now reports only the 32 shared baseline/generated-file issues, and `make wire-check` reports only the unregistered generated Wire file. No shared inventory was edited.
