# Agent CLI Docs

This is the local entrypoint for Agent CLI guides, fixture notes, prompt references, and contributor planning material under `libraries/agent-cli/docs/`.

## CLI Users

- [Agent session record and replay](session-record-replay.md) explains how to record live Grok and OpenAI Realtime sessions, replay captures without provider network calls, and interpret replay divergence errors.
- [Files TODO](FILES.md) describes planned provider file-upload behavior and the proposed `agent files` commands for managing uploaded files.

## Fixture and Test Authors

- [Agent session record and replay](session-record-replay.md) covers capture sanitization, committed fixture locations, capture fields, and replay divergence cases for session replay tests.

## Agent CLI Contributors

- [Notes](NOTEs.md) collects implementation ideas and follow-up work for Agent CLI behavior, local models, multimodal output, skills, and error handling.
- [Files TODO](FILES.md) outlines the planned internal provider-file API integration, local cache shape, stream events, and task breakdown.
- [Looper prompt](prompts/LOOPER.md) is the current prompt reference under `prompts/` for administrator-style loop execution.
- Additional prompt references should live under `prompts/` when new prompt guides are added.
