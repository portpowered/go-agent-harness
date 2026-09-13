## Summary

- Retire CLI-owned playback/capture observer projection and overflow reporting into the public `go-agent-runtime/services/devices` contract, private implementation, and registered Wire provider.
- Keep the CLI file as decision-free compatibility adapters and update only the two admitted integration callers.
- Add focused observer coverage, a GOWORK=off public consumer, bounded shipped loopback evidence, and the existing audio/tool replay regression.

## Verification

- Focused normal/race tests, full devices normal/race tests, Wire and coverage registration, vet, staticcheck, and lint pass.
- The external consumer passes with `GOWORK=off` in normal and race modes.
- Shipped `yui` plus `audio-device-server` software loopback and the existing C16 audio/tool replay pass with exact PCM/loss evidence.

## Deferred shared gate

`make architecture-size-check` reports exactly these two downward stale entries in the shared baseline:

- `session_playback_diagnostics.go:fallbackPlaybackDiagnosticSink`
- `session_playback_diagnostics.go:playbackMetricSamples`

C61 retains the active shared baseline lease. This branch deliberately does not edit `docs/architecture/architecture-size-baseline.json`; after C61 releases it, the same task will delete only those two entries, rerun the architecture and focused gates, and then submit the same PR to script CI. Script CI, independent review, guarded merge, and any vertical probe are not claimed here.
