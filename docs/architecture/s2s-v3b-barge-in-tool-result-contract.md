# Barge-in during tool call — tool-result contract (s2s v3b)

## Invariant

When user audio barges in while a tool call is in flight, the pending tool
result has exactly two acceptable outcomes:

1. **Delivered** — the result reaches the provider (observed on the outbound
   client-to-provider path, e.g. a `conversation.item.create` carrying the
   function-call output for the issued `call_id`).
2. **Explicitly discarded** — the loop drops the result through an observable
   discard event (`tool.result.discarded` naming the `call_id`).

Any other outcome — a result that silently vanishes after the barge-in — is an
**orphaned tool result** and fails the run.

## How v3b enforces it

The contract is enforced by CLI-verified hermetic replay at the T1 tier: every
scenario drives the real `agent probe run` command over recorded session
fixtures; no internal Go function calls count as evidence.

Three replay fixtures live in `agent-cli/test/integration/testdata/`:

| fixture | scenario | expected outcome |
|---|---|---|
| `s2s-v3b-barge-in-tool-result-delivered.session.json` | `s2s-v3b-barge-in-tool-result-delivered` | passes: result delivered, no orphans, clean exit |
| `s2s-v3b-barge-in-tool-result-discarded.session.json` | `s2s-v3b-barge-in-tool-result-discarded` | passes: explicit discard event, clean exit |
| `s2s-v3b-barge-in-tool-result-orphaned.session.json` | `s2s-v3b-barge-in-tool-result-orphaned` | negative control: non-zero exit naming the orphaned call |

The measurable expectation vocabulary added to the probe runner:

- `tool_result_delivered` / `tool-result-delivered`
- `tool_result_discarded` / `tool-result-discarded`
- `no_orphaned_tool_result` / `no-orphaned-tool-result`

`no-orphaned-tool-result` is the standing gate: every tool call observed in the
session must appear as either delivered or explicitly discarded, otherwise the
expectation fails with the offending `call_id`s listed.
