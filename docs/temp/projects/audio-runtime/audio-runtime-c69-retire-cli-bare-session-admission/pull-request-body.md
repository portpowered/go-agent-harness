## Summary

- Extract bare live-session admission into the provider-neutral `go-agent-runtime/services/bareadmission` contract, private implementation, and dedicated Wire graph.
- Reduce `session_bare.go` to a Deprecated CLI-edge adapter that snapshots CLI-owned inputs, delegates once, and maps results/errors back.
- Preserve provider/model/transport precedence, OpenAI/Grok defaults, typed error identity and redacted credential wording, VAD/transcription/device policy, immutability, and early failure behavior.
- Add an independent `GOWORK=off` consumer and focused normal/race regressions.

## Provenance

- Project: `audio-runtime/audio-runtime-v1`
- Startup integration: `8bdafc7f947a3a2c9856220abdc539437035bd21`
- Planning/fetched `origin/main`: `d5d6f84363d8569d5dc1a59985f8d45cf50e1d06`
- Accepted-main `session_bare.go`: 271 lines
- Candidate `session_bare.go`: 222 lines, net production retirement 49 lines

## Focused validation

- Bare-admission normal: 16 tests across 3 packages
- Bare-admission race: 48 tests across 3 packages
- CLI compatibility normal/race: 27 tests each
- Independent consumer normal/race: pass with `GOWORK=off`
- `make fmt`, `make vet`, pinned golangci-lint v2.9.0, pinned staticcheck 2026.1: pass
- Coverage registration: 179 packages across 6 modules
- Full `coverage-changed` target including the 80% bare-admission floor: pass

## Handoff state

Architecture and Wire checks have one shared registry finding, and the size
check has that same finding plus five stale entries for retired C69 CLI
symbols. `scripts/wire-packages.txt` is under the active C57 lease and
`docs/architecture/architecture-size-baseline.json` is under the active C61
lease. They are intentionally unchanged here. After those exact owners release
the files, this same task should integrate current accepted main, make only the
downward Wire registration and five stale-entry deletions, rerun the gates, and
submit the same head to script CI without polling.

This PR does not claim green CI, independent review, guarded merge, or whole-
project acceptance.
