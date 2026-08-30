# Agent CLI Docs

This is the local entrypoint for Agent CLI guides, fixture notes, prompt references, and contributor planning material under `agent-cli/docs/`.

## CLI Users

- [PNIG interaction replay](interaction-replay.md) explains how to replay normalized interaction fixtures as NDJSON without provider credentials or live network calls.
- [Agent session record and replay](session-record-replay.md) explains how to record live Grok and OpenAI Realtime sessions, replay captures without provider network calls, and interpret replay divergence errors.
- [Live audio-in round-trip proof](session-audio-in-live.md) documents the bounded spoken-audio proof and the per-session input-transcription cost policy.
- [Blind acceptance probes](acceptance-probe.md) explains the artifact-backed verdict contract and live/replay transport seam.
- [Conversational customer simulation](customer-simulation-live.md) documents the explicit billed live suite, A/B/D selectors, safe key setup, audio layout, and hermetic versus live reruns.
- [Room live visualizer](room-visualizer.html) is a zero-build browser page for the room's transcript, diagnostic, and lifecycle SSE events.
- [Three-participant room fixture](room-three-participant.json) is a credential-free manifest with one opening seed and distinct Realtime voices.
- [Tool-assistant room fixture](room-tool-assistant.json) is a bounded two-participant manifest with a tool-less customer and an exec-only assistant.
- [Tool-assistant room live acceptance](room-tool-assistant-live-acceptance.md) documents the credential-safe billed run, sanitized evidence checks, and exact proof-file verification.
- [Room Phase 1 live acceptance](room-live-acceptance.md) documents the billed/non-CI three-participant smoke procedure, artifact inspection, C1 wedge rule, and honest scenario ledger.
- [Files TODO](FILES.md) describes planned provider file-upload behavior and the proposed `agent files` commands for managing uploaded files.

## Fixture and Test Authors

- [PNIG interaction replay](interaction-replay.md) covers the normalized interaction fixture envelope, validation behavior, and CLI inspection flow for gateway event fixtures.
- [Agent session record and replay](session-record-replay.md) covers capture sanitization, committed fixture locations, capture fields, and replay divergence cases for session replay tests.
- [Live scheduled-turn boundary confirmation](../../docs/architecture/s2s-live-turn-boundary-commit-races.md) gives the opt-in OpenAI procedure for delayed acknowledgement and speech-then-exact-silence checks.
- [S2S audio tool-turn lifecycle](../../docs/architecture/s2s-audio-tool-turn-lifecycle.md) defines the shared call-ID lifecycle, scheduled-turn/close gates, hermetic proof surface, and credential-safe live tool confirmation.
- Shared committed `.session.json` replay fixtures are owned by `go-llm-gateway/pkg/testing/testdata/session-fixtures`; keep `agent-cli/test/integration/testdata` for CLI-private fixtures only, and use `go-llm-gateway/pkg/testing.SharedSessionFixturePath(...)` when Agent CLI tests need the shared canonical fixtures.

## Agent CLI Contributors

- [Notes](NOTEs.md) collects implementation ideas and follow-up work for Agent CLI behavior, local models, multimodal output, skills, and error handling.
- [Files TODO](FILES.md) outlines the planned internal provider-file API integration, local cache shape, stream events, and task breakdown.
- [Looper prompt](prompts/LOOPER.md) is the current prompt reference under `prompts/` for administrator-style loop execution.
- Additional prompt references should live under `prompts/` when new prompt guides are added.
