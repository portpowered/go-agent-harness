# WebMCP live-session result trace

Status: story 001 diagnosis recorded; the freshness enforcement and scope
regressions are subsequent stories in `prd.json`.

## Controlled reproduction

`agent-cli/internal/cli/webmcp_query_trace_test.go` uses one scripted browser,
one selected target, generation 1, one catalog reference, and `{}` for both
calls. The fixture first publishes a completed `EventToolResponded` for the
next protocol ID with:

```json
{"count":0,"documents":[]}
```

The live first-class page-tool executor then dispatches `list_documents` with
that ID. The direct path invokes the same ref and input against the same
selected target; the fixture publishes:

```json
{"count":1,"documents":[{"id":"welcome-to-margin","title":"Welcome to Margin"}]}
```

The trace asserts the browser, target, frame, generation, tool name, input,
and catalog reference at resolution and dispatch, and checks the published
event order. There is no model, network, or external API in this reproduction.

## Confirmed mechanism and first divergence

The first divergence is broker-side result reconciliation, not catalog
selection, readiness, or page state. `EventToolResponded` is accepted by
protocol invocation ID in
`agent-cli/internal/webmcp/broker_invocations.go:658-676`; if it arrives
before the matching invocation event, it is retained in the ID-only
`earlyTerminals` buffer. After the target command returns, the dispatch path
consumes that buffer at
`agent-cli/internal/webmcp/broker_invocations.go:494-502`, before the queued
`EventToolInvoked` has supplied frame/tool/input provenance. The result is then
serialized unchanged by `agent-cli/internal/webmcp/tools/tools.go:947-986`.

Chrome broadcasts WebMCP events to attached DevTools clients, so a terminal
from another direct client can be observed by the live-session broker. A
reused protocol ID in the same generation therefore lets the other client's
empty result become the current live call's successful payload. The fixture
holds the catalog and page generation constant and has no result cache; the
only changed boundary is the pre-invocation terminal event. This rules out
catalog/generation freshness, readiness ordering, and result caching for this
reproduction.

## Query-surface audit for the supplied Margin catalog

The currently exercised Margin page catalog exposes these page tools:

| Tool | Classification | Story-001 finding |
| --- | --- | --- |
| `get_document` | query/read | Shares the broker invocation/result path; no separate reproduction yet |
| `list_documents` | query/read | Affected in the controlled reproduction |
| `list_comments` | query/read | Shares the broker invocation/result path; no separate reproduction yet |
| `add_comment` | mutation | Not a query result; preserve side-effect/cancellation semantics |
| `create_document` | mutation | Not a query result; preserve side-effect/cancellation semantics |
| `open_document` | page action | Not classified as a query result |
| `reopen_comment` | mutation | Not a query result; preserve side-effect/cancellation semantics |
| `reply_to_comment` | mutation | Not a query result; preserve side-effect/cancellation semantics |
| `resolve_comment` | mutation | Not a query result; preserve side-effect/cancellation semantics |
| `update_document` | mutation | Not a query result; preserve side-effect/cancellation semantics |

Thus the affected set confirmed by this reproduction is only
`list_documents`; the other read tools use the same shared boundary and must
be covered by the freshness fix and behavioral scope audit rather than being
declared unaffected from a name-only inventory.
