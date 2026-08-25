# Session Fixture Authoring Guide

---
author: Port OS Team
last modified: 2026, april, 12
component: go-llm-gateway
---

This guide defines the review contract for committed `.session.json` captures used by `go-llm-gateway/pkg/testing`. Use it before adding, regenerating, or reviewing session replay fixtures.

`go-llm-gateway/pkg/testing` is the authoritative owner for committed shared `.session.json` replay fixtures in this repository. This guide and the validator under `go-llm-gateway` define the canonical contract because the gateway already owns the replay format, replay helpers, fixture provenance rules, and hygiene validation behavior.

This ownership decision satisfies the Phase 2 enabling step for session fixture ownership and boundary cleanup before broader API hardening.

## Ownership Boundary

Use these two fixture classes deliberately:

- Shared committed session fixtures live under `go-llm-gateway/pkg/testing/testdata/session-fixtures`. They are the canonical replay fixtures that other modules may consume when they need repository-shared `.session.json` behavior.
- Module-local fixtures stay in the package that owns the behavior under test, such as `agent-cli/test/integration/testdata` for CLI-private command scenarios. Sibling modules must not treat another package's private `testdata` as the canonical shared fixture source.

When a CLI fixture becomes broadly useful for replay or hygiene validation across packages, promote a sanitized copy into the gateway-owned shared fixture root instead of teaching other modules to reach into Agent CLI private `testdata`.

## Required Metadata

Every committed `.session.json` capture must use the versioned `SessionCapture` envelope and include `session.fixture_provenance`.

```json
{
  "version": 1,
  "provider": {
    "name": "grok",
    "model": "grok-realtime"
  },
  "session": {
    "id": "sess_sanitized",
    "started_at_utc": "2026-04-11T00:00:00Z",
    "fixture_provenance": "synthetic"
  },
  "records": []
}
```

`session.fixture_provenance` tells reviewers how the capture was produced and which safety checks apply.

Run the validator before review from `go-llm-gateway`:

```sh
go run ./cmd/session-fixture-validator ./pkg/testing/testdata/session-fixtures
```

Adding or removing a committed fixture anywhere under the registered roots
requires regenerating the checked-in fixture manifest from the repository root:

```sh
go run ./go-llm-gateway/cmd/session-fixture-validator -emit-manifest go-llm-gateway/internal/sessionfixturevalidator/testdata/committed-fixtures.manifest.json go-llm-gateway/pkg/providers/openai/testdata go-llm-gateway/pkg/testing/testdata/session-fixtures agent-cli/test/integration/testdata
```

Until you do, `TestAllCommittedSessionFixturesPassWithExactCount` fails naming
the drifted paths and this command.

## Provenance Categories

Use one of these provenance categories for committed fixtures:

| Category | Use When | Review Expectation |
|----------|----------|--------------------|
| `synthetic` | The fixture was hand-authored or generated from fake test data. | Payloads must contain no real provider traffic, raw audio, credentials, tokens, cookies, passwords, secrets, or other sensitive values. |
| `provider_recorded` | The fixture was captured from a real provider session and then sanitized. | Provider metadata may identify the provider and model, but session IDs, user content, credentials, raw audio, and account-specific values must be removed or replaced with deterministic safe values. |

Do not commit fixtures with ambiguous provenance such as `unknown`, blank metadata, or comments that only explain the source outside the JSON file. The capture must carry its own provenance.

## Sanitization Rules

Synthetic fixtures must never include raw audio or credential-like fields anywhere in `records[*].payload`.

Do not commit synthetic payload fields named:

- `audio`
- `audio_bytes`
- `input_audio`
- `authorization`
- `api_key`
- `token`
- `password`
- `secret`
- `cookie`
- `set_cookie`

Apply the same review posture to close variants that clearly carry credentials or raw media, even when the field name is not listed exactly. Prefer small text deltas, fake session IDs, and deterministic placeholder values that prove replay behavior without preserving live user or provider data.

Provider-recorded fixtures must be sanitized before commit. Replace provider session IDs, request IDs, account IDs, user content, and timestamps when those values are not required for the test assertion. Never commit API keys, authorization headers, bearer tokens, cookies, passwords, secrets, or raw audio captures. The validator rejects credential-like keys and values for every fixture provenance, not only synthetic fixtures.

## Payload Type Distinction

Session captures can store normalized gateway events or raw provider wire events. The `payload_type` field must make the distinction explicit.

Use `payload_type: "stream_message"` for generic session events whose `payload` is a serialized `messages.StreamMessage`. These records use normalized event types such as `SESSION.CREATED`, `SESSION.UPDATE`, `TEXT.DELTA`, and `AUDIO.DELTA`.

Use `payload_type: "websocket_message"` for provider wire captures whose `payload` is raw provider WebSocket JSON. These records use provider event types such as OpenAI Realtime `session.created`, `session.update`, `conversation.item.create`, `response.create`, `response.output_text.delta`, and `response.output_audio.delta`.

Do not encode provider wire records as `payload_type: "stream_message"`. That makes replay fixtures look provider-agnostic when they actually depend on provider WebSocket semantics.

## Review Checklist

Before committing a `.session.json` fixture:

1. Confirm the file uses the `SessionCapture` envelope with `version`, `provider`, `session`, and `records`.
2. Confirm `session.fixture_provenance` is present and set to `synthetic` or `provider_recorded`.
3. Confirm synthetic fixtures contain no raw audio fields or credential-like keys anywhere under `records[*].payload`.
4. Confirm provider-recorded fixtures have sanitized provider session IDs, request IDs, account values, user content, credentials, cookies, and raw audio.
5. Confirm normalized gateway records use `payload_type: "stream_message"`.
6. Confirm raw provider WebSocket records use `payload_type: "websocket_message"`.
7. Confirm the fixture can be replayed without live provider credentials, network calls, microphones, or audio devices.
8. Confirm replay divergence errors identify expected and actual event types without printing raw sensitive payloads.
