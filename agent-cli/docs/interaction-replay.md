# PNIG Interaction Replay

Use `agent interaction replay` to inspect a normalized PNIG interaction fixture without provider credentials or live network access.

## Usage

Replay a fixture and print one JSON object per line to stdout:

```bash
agent interaction replay fixtures/demo.interaction.json
```

Each line is one normalized `InteractionEvent` in fixture sequence order. This makes the command easy to inspect with tools like `jq`, `sed`, or `rg`.

```bash
agent interaction replay fixtures/demo.interaction.json | jq .
```

## Behavior

- The fixture path must point to a valid PNIG interaction fixture JSON file.
- The command validates the fixture before replaying any events.
- Invalid fixtures exit non-zero and include the fixture path plus the first invalid field in the error.
- Replay is credential-free: it does not read provider API keys or call live provider endpoints.

## Fixture Shape

PNIG interaction fixtures use the `gateway.interaction.v1` envelope defined in `go-llm-gateway/pkg/gateway`.

```json
{
  "version": "gateway.interaction.v1",
  "request": {
    "interactionId": "int_demo",
    "provider": "fixture",
    "model": "demo-model"
  },
  "events": [
    {
      "interactionId": "int_demo",
      "sequence": 1,
      "type": "interaction.start",
      "provider": "fixture",
      "model": "demo-model"
    }
  ]
}
```

See `go-llm-gateway/pkg/gateway/interaction_fixture.go` for the validation rules and the supported event payloads.
