# Agent CLI Docs

This is the local entrypoint for Agent CLI guides, fixture notes, prompt references, and contributor planning material under `agent-cli/docs/`.

## CLI Users

- [PNIG interaction replay](interaction-replay.md) explains how to replay normalized interaction fixtures as NDJSON without provider credentials or live network calls.
- [Agent session record and replay](session-record-replay.md) explains how to record live Grok and OpenAI Realtime sessions, replay captures without provider network calls, and interpret replay divergence errors.
- [Files TODO](FILES.md) describes planned provider file-upload behavior and the proposed `agent files` commands for managing uploaded files.

## Fixture and Test Authors

- [PNIG interaction replay](interaction-replay.md) covers the normalized interaction fixture envelope, validation behavior, and CLI inspection flow for gateway event fixtures.
- [Agent session record and replay](session-record-replay.md) covers capture sanitization, committed fixture locations, capture fields, and replay divergence cases for session replay tests.
- Shared committed `.session.json` replay fixtures are owned by `go-llm-gateway/pkg/testing/testdata/session-fixtures`; keep `agent-cli/test/integration/testdata` for CLI-private fixtures only, and use `go-llm-gateway/pkg/testing.SharedSessionFixturePath(...)` when Agent CLI tests need the shared canonical fixtures.

## Agent CLI Contributors

- [Notes](NOTEs.md) collects implementation ideas and follow-up work for Agent CLI behavior, local models, multimodal output, skills, and error handling.
- [Files TODO](FILES.md) outlines the planned internal provider-file API integration, local cache shape, stream events, and task breakdown.
- [Looper prompt](prompts/LOOPER.md) is the current prompt reference under `prompts/` for administrator-style loop execution.
- Additional prompt references should live under `prompts/` when new prompt guides are added.
