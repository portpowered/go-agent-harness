# WebMCP live-session result trace

Status: stories 001–004 diagnosis, freshness enforcement, parity, and query
scope regression recorded.

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
`agent-cli/internal/webmcp/broker_invocations.go:704-725`; if it arrives
before the matching invocation event, it is retained in the ID-only
`earlyTerminals` buffer. After the target command returns, the dispatch path
consumes that buffer at
`agent-cli/internal/webmcp/broker_invocations.go:541-547`, before the queued
`EventToolInvoked` has supplied frame/tool/input provenance. Before this fix,
that state transition handed the terminal payload to the caller; the shared
serialization boundary is
`agent-cli/internal/webmcp/tools/tools.go:995-1042`.

Chrome broadcasts WebMCP events to attached DevTools clients, so a terminal
from another direct client can be observed by the live-session broker. A
reused protocol ID in the same generation therefore let the other client's
empty result become the current live call's successful payload. The fixture
holds the catalog and page generation constant and has no result cache; the
only changed boundary is the pre-invocation terminal event. This rules out
catalog/generation freshness, readiness ordering, and result caching for this
reproduction. The corrected owning guard is
`agent-cli/internal/webmcp/broker_freshness.go:11-37`; it requires the exact
invocation event before a terminal can be decoded, and the dispatch collision
fence is at `agent-cli/internal/webmcp/broker_invocations.go:473-508`.

Unproven query terminals now produce a classified `webmcp.tool-result.v1`
failure with `phase=result_freshness` and retryable read-only guidance. An
unproven mutation remains side-effect-unknown and explicitly says not to
retry until target state is reconciled.

## Query-surface audit for the supplied Margin catalog

The currently exercised Margin page catalog exposes these page tools:

| Tool | Classification | Incident and scope finding |
| --- | --- | --- |
| `get_document` | query/read | No separate incident reproduction; covered as a read-only member of the shared corrected broker path by the table-driven scope regression |
| `list_documents` | query/read | Affected in the controlled reproduction; live/direct scope and payload-parity regression |
| `list_comments` | query/read | No separate incident reproduction; covered as a read-only member of the shared corrected broker path by the table-driven scope regression |
| `add_comment` | mutation | Not a query result; preserve side-effect/cancellation semantics |
| `create_document` | mutation | Not a query result; preserve side-effect/cancellation semantics |
| `open_document` | page action | Not classified as a query result |
| `reopen_comment` | mutation | Not a query result; preserve side-effect/cancellation semantics |
| `reply_to_comment` | mutation | Not a query result; preserve side-effect/cancellation semantics |
| `resolve_comment` | mutation | Not a query result; preserve side-effect/cancellation semantics |
| `update_document` | mutation | Not a query result; preserve side-effect/cancellation semantics |

The incident-specific affected set confirmed by the reproduction is only
`list_documents`. The corrected-boundary scope is all three query/read tools:
`get_document`, `list_documents`, and `list_comments`, because all three are
catalog descriptors with `read_only: true` and invoke through the same broker
provenance guard. There are no query tools classified as unaffected by a
different ownership or correlation path in this catalog. The table-driven
`TestWebMCPQueryToolScopeUsesFreshnessGuard` exercises each read descriptor
through both the first-class live executor and direct CLI adapter with a
representative non-empty payload, and compares decoded payloads. Mutation and
page-action tools are excluded from that matrix; their existing side-effect,
cancellation, timeout, and stale-reference behavior remains covered by the
broker and operation suites.
