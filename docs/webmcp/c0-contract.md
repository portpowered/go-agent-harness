# WebMCP C0 contract

**Status:** normative contract for the in-process, CLI-owned WebMCP broker
**Contract owner:** the C0 integration owner
**Scope:** the broker tools, their textual result envelope, and stable
model-facing errors

This document freezes the broker surface that a session gives to a model. It
does not implement browser discovery, Chrome/CDP behavior, session wiring, or
provider schema evolution. Those later changes MUST consume this contract and
MUST use an additive amendment reviewed by the C0 integration owner when they
need a new shared semantic.

The words **MUST**, **MUST NOT**, **SHOULD**, and **MAY** are normative. JSON
blocks marked **Normative example** are valid wire values, but their IDs and
page data are illustrative.

## 1. Broker tools and textual result envelope

### 1.1 Stable model-facing tools

The broker advertises exactly these six ordinary function tools. Their names,
parameter names, scalar types, and required status are part of the contract.
The provider-facing definitions MUST remain unchanged when the selected page's
catalog changes.

| Tool | Parameter | Type | Required | Omitted value / meaning |
| --- | --- | --- | --- | --- |
| `webmcp_get_context` | `refresh` | boolean | no | `false`; refresh browser and target metadata when true |
| `webmcp_list_tabs` | `browser_id` | string | no | empty; include all discovered browsers |
|  | `origin_contains` | string | no | empty; do not filter by origin substring |
|  | `eligible_only` | boolean | no | `true`; return only targets eligible for WebMCP |
|  | `include_zero_tool_pages` | boolean | no | `false`; include eligible pages with zero tools when true |
| `webmcp_select_tab` | `browser_id` | string | yes | exact discovered browser identifier |
|  | `target_id` | string | yes | exact target identifier from the selected browser |
|  | `activate` | boolean | no | `false`; selection MUST NOT activate the page unless true |
| `webmcp_list_tools` | `refresh` | boolean | no | `false`; refresh the selected page catalog when true |
|  | `name_contains` | string | no | empty; do not filter by tool name |
|  | `include_schemas` | boolean | no | `true`; include complete page input schemas |
|  | `frame_id` | string | no | empty; include tools from every eligible frame |
| `webmcp_invoke` | `tool_ref` | string | yes | opaque reference returned by `webmcp_list_tools` |
|  | `input_json` | string | yes | UTF-8 JSON object encoded as a string |
|  | `reason` | string | yes | brief user-facing reason for the requested action |
| `webmcp_cancel` | `invocation_id` | string | yes | invocation identifier returned by the broker |
|  | `reason` | string | no | empty; optional user-facing cancellation reason |

Every definition MUST have an object parameter schema with
`"additionalProperties": false`. The only accepted properties are the
parameters in the table above. An unknown property MUST be rejected before the
broker operation runs. A missing required property MUST be rejected as
`invalid_tool_input` for the relevant broker call. The broker MUST preserve
JSON number tokens in `input_json`; it MUST NOT round-trip them through a
binary floating-point value before dispatch.

The definitions are intentionally flat: each property is a boolean or string
scalar. `webmcp_invoke` does not accept a nested `input` object, a
`tool_name` alternative, or a schema-shaped `oneOf`. The model supplies the
page-tool arguments in `input_json`, where they are validated against the
selected page descriptor before a page command is dispatched.

The following is a **Normative example** of the complete definition shape for
the two calls whose input boundary is easiest to confuse:

```json
[
  {
    "name": "webmcp_invoke",
    "description": "Invoke one page tool from the current catalog.",
    "parameters": {
      "type": "object",
      "properties": {
        "tool_ref": {
          "type": "string",
          "description": "Opaque reference returned by webmcp_list_tools."
        },
        "input_json": {
          "type": "string",
          "description": "UTF-8 JSON object containing the page-tool arguments."
        },
        "reason": {
          "type": "string",
          "description": "Brief user-facing reason for the action."
        }
      },
      "required": ["tool_ref", "input_json", "reason"],
      "additionalProperties": false
    }
  },
  {
    "name": "webmcp_cancel",
    "description": "Cancel a pending WebMCP invocation.",
    "parameters": {
      "type": "object",
      "properties": {
        "invocation_id": { "type": "string" },
        "reason": { "type": "string" }
      },
      "required": ["invocation_id"],
      "additionalProperties": false
    }
  }
]
```

The other four definitions use the same object shape and the exact scalar
properties and `required` list from the table. `webmcp_cancel` remains in the
stable six-tool definition set even when policy does not permit cancellation;
that call then returns a classified error rather than changing the provider's
definition list.

`tool_ref` is opaque. It MUST be obtained from the current
`webmcp_list_tools` result and MUST be bound by the broker to the browser,
target, frame, page generation, page tool name, and schema digest represented
by that result. A page generation change invalidates the reference. A caller
MUST NOT infer identity from a page tool name or trust an encoded reference
without checking the current catalog.

### 1.2 Textual result envelope

Every broker execution MUST produce one compact UTF-8 JSON object in
`messages.ToolCallResponse.Content`. The outer tool response retains the model
call ID (and, where the surrounding executor supplies it, the tool name) for
correlation. Broker results MUST use this textual path; they MUST NOT put the
browser result in `ContentParts` or a rich-result message.

The envelope has exactly four top-level members:

```text
version: string
ok: boolean
data: JSON value or null
error: object or null
```

`version` MUST equal `webmcp.tool-result.v1`. A successful envelope MUST have
`ok: true`, a non-null operation-specific `data` value, and `error: null`. A
failed envelope MUST have `ok: false`, `data: null`, and a non-null error
object. Producers MUST NOT append prose, a second JSON value, or a delimiter
outside this object. Consumers MUST reject an unknown envelope version rather
than guessing its fields.

**Normative example — successful invocation:**

```json
{
  "version": "webmcp.tool-result.v1",
  "ok": true,
  "data": {
    "browser_id": "chrome-local-1",
    "target_id": "tab-1",
    "generation": 7,
    "tool_ref": "wmcp1_example",
    "tool_name": "read_state",
    "status": "completed",
    "output": { "value": 42 }
  },
  "error": null
}
```

`data.output` is one JSON value owned by the page. Objects and arrays remain
objects and arrays; a broker MUST NOT JSON-stringify that value a second time.
The value is untrusted page content and MUST NOT be inserted into system
instructions. An output value of `null` is valid page output and is distinct
from the envelope's failed `data: null` shape.

`webmcp_list_tools` returns complete page descriptors under
`data.tools[].input_schema` when `include_schemas` is true (the default). The
schema is result data, not a provider definition. A **Normative example** is:

```json
{
  "version": "webmcp.tool-result.v1",
  "ok": true,
  "data": {
    "generation": 7,
    "tools": [
      {
        "ref": "wmcp1_example",
        "name": "read_state",
        "description": "Read fixture state",
        "input_schema": {
          "type": "object",
          "properties": {},
          "additionalProperties": false
        },
        "annotations": { "read_only": true },
        "frame": { "id": "frame-1", "origin": "https://fixture.test" },
        "generation": 7
      }
    ]
  },
  "error": null
}
```

When `include_schemas` is false, the broker MAY omit `input_schema` to reduce
result size; a caller that needs to construct or validate page input MUST call
with `include_schemas: true`. Page descriptions, schemas, annotations, URLs,
arguments, and outputs are untrusted data. Implementations MUST apply the
configured result-size and recording redaction policy at their boundaries.

**Normative example — failed invocation:**

```json
{
  "version": "webmcp.tool-result.v1",
  "ok": false,
  "data": null,
  "error": {
    "code": "invalid_tool_input",
    "message": "Input does not match the selected tool schema.",
    "retryable": true,
    "details": {
      "tool_ref": "wmcp1_example",
      "input_schema": {
        "type": "object",
        "properties": {
          "count": { "type": "integer" }
        },
        "required": ["count"],
        "additionalProperties": false
      },
      "issues": [
        { "path": "/count", "code": "required" }
      ]
    }
  }
}
```

The error object has exactly `code`, `message`, `retryable`, and `details`.
`message` MUST be concise and safe for model and user display; raw endpoint
credentials, raw page exception text, input values, and unredacted sensitive
URLs MUST NOT appear in it. `details` MUST be a JSON object whose allowed
shape is defined below. A `retryable: true` value means that the caller may
retry after applying the remediation represented by `details`; it does not
authorize an automatic duplicate of an operation whose side effect is
unknown. `retryable: false` means no transparent retry is safe, though a user
may make a fresh, informed request after recovery.

## 2. Classified error contract

The following codes are the complete stable model-facing vocabulary for the C0
broker. They are ordinary correlated tool results and MUST NOT terminate the
agent session by default. Error details are deliberately bounded: IDs are
normalized opaque identifiers, origin information is a digest or origin-only
value, and page/provider secrets are excluded.

The notation `?` means an optional key. Arrays of candidate IDs MUST contain
only normalized IDs. `origin_digest` is a stable redacted digest and MUST NOT
be replaced with a query-bearing URL. `issues` contains JSON Pointer paths and
stable validation codes, never the offending input value.

| Code | Trigger | Retryable | Required safe `details` shape |
| --- | --- | --- | --- |
| `webmcp_disabled` | A broker operation is requested without explicit browser-tool activation. | true | `{"activation":"browser-tools"}` |
| `endpoint_not_found` | The requested explicit endpoint or discovery source has no browser endpoint. | false | `{"endpoint_kind":string,"source":string}` |
| `endpoint_unreachable` | The configured endpoint cannot be contacted before a browser session is established. | true | `{"endpoint_kind":string,"address_class":"loopback"|"non_loopback","phase":string}` |
| `remote_endpoint_denied` | A non-loopback endpoint was supplied without the explicit remote-endpoint permission. | false | `{"endpoint_kind":string,"network_class":"non_loopback","required_flag":"browser-allow-remote-cdp"}` |
| `browser_protocol_invalid` | The endpoint response or protocol metadata cannot be parsed or fails the required protocol checks. | false | `{"phase":string,"protocol":string,"reason_code":string}` |
| `unsupported_webmcp` | The selected browser/target does not provide the required WebMCP domain or capability. | false | `{"browser_id":string,"target_id":string,"required_capability":"webmcp"}` |
| `no_eligible_tab` | No target satisfies the requested selection and eligibility filters. | true | `{"browser_id":string? ,"filters":object,"candidate_count":number}` |
| `ambiguous_browser` | Browser discovery returns multiple candidates and no exact browser ID was supplied. | true | `{"candidate_browser_ids":[string,...]}` |
| `ambiguous_tab` | A browser has multiple matching targets and no exact target ID was supplied. | true | `{"browser_id":string,"candidate_target_ids":[string,...]}` |
| `stale_selection` | The selected browser or target no longer exists at the expected selection generation. | true | `{"browser_id":string,"target_id":string,"selected_generation":number,"reason":string}` |
| `stale_tool_ref` | `tool_ref` belongs to a page generation that is no longer current. | true | `{"tool_ref":string,"current_generation":number,"refresh_required":true}` |
| `origin_denied` | The origin is excluded by the configured browser policy. | false | `{"origin_digest":string,"policy":"allowed_origins"|"denied_origins"}` |
| `approval_required` | The operation is not approved for the exact browser, origin, target, generation, and tool scope. | true | `{"browser_id":string,"target_id":string,"origin_digest":string,"generation":number,"tool_ref":string,"approval_scope":"exact_tool"}` |
| `approval_denied` | The user or approval provider explicitly denied the requested operation. | false | `{"browser_id":string,"target_id":string,"origin_digest":string,"generation":number,"tool_ref":string,"decision":"denied"}` |
| `invalid_tool_input` | `input_json` is malformed, not an object, contains unknown broker properties, or fails the selected page schema. | true | `{"tool_ref":string,"input_schema":object,"issues":[{"path":string,"code":string},...]}` |
| `result_too_large` | A completed broker/page result exceeds the configured serialized-result limit. | false | `{"tool_ref":string,"limit_bytes":number,"observed_bytes":number}` |
| `target_attach_failed` | The broker cannot attach or initialize the selected target before dispatch. | true | `{"browser_id":string,"target_id":string,"phase":string,"reason_code":string}` |
| `target_detached` | The selected target detached while an operation was in progress. | false | `{"browser_id":string,"target_id":string,"generation":number,"reason":string}` |
| `page_navigated` | The page generation changed during an operation, invalidating its catalog context. | false | `{"browser_id":string,"target_id":string,"previous_generation":number,"current_generation":number}` |
| `invocation_failed` | The page handler or browser command returned a failure after invocation was dispatched. | false | `{"invocation_id":string,"tool_ref":string,"phase":string,"page_error_code":string,"side_effect_unknown":true}` |
| `invocation_canceled` | The invocation was canceled by the user, policy, session, or explicit cancel call. | false | `{"invocation_id":string,"cancel_source":string}` |
| `invocation_timed_out` | The invocation exceeded its configured deadline. | false | `{"invocation_id":string,"timeout_ms":number,"phase":string,"side_effect_unknown":true}` |
| `invocation_orphaned` | The target or broker closed before a terminal page response could be observed. | false | `{"invocation_id":string,"target_id":string,"generation":number,"terminal_observed":false}` |
| `browser_disconnected` | The browser connection ended before the broker could complete the operation or reconcile it. | false | `{"browser_id":string,"target_id":string?,"phase":string,"reconnect_required":true}` |

`invalid_tool_input` MUST return the selected `tool_ref` and the complete
selected `input_schema`, even when the error is caused by malformed
`input_json`; the schema is what allows the model to self-correct. The broker
MUST distinguish malformed JSON/schema mismatch from a page handler failure.
For an invocation that may have reached the page, timeout, disconnect,
detachment, navigation, and handler-failure codes intentionally do not permit
an automatic retry because repeating a mutating action could duplicate an
unknown side effect. A caller SHOULD refresh context and list tools before
asking the user whether to retry.

No error detail may include a CDP authorization token, a raw websocket URL, a
credential-bearing endpoint, raw invocation arguments, raw page output, or
unredacted query/fragment data. Candidate IDs and operation IDs are safe to
return because they are opaque broker identifiers.

## 3. Supersession and rationale

This section is part of the contract because it records why the boundary is
deliberately less expressive than a page schema.

* The accepted architecture selects an in-process CLI-owned broker and keeps
  stable model definitions flat; page schemas travel in result data. It
  explicitly replaces the source plan's nested `tool_ref`/`tool_name` choice
  with `tool_ref string`, `input_json string`, and `reason string`.
  See [Architecture Decision §1](architecture-decision.md#1-tool-schema-contract)
  and [§4](architecture-decision.md#4-overall-topology).
* The source-plan `webmcp_invoke` example in
  [§10.5](source-plan.md#105-webmcp_invoke), which accepts a `tool_name`
  alternative and a nested `input` object, is explicitly superseded by §1.1
  of this document. The source-plan statement that `webmcp_cancel` is merely
  optional is also superseded: it is always one of the six stable definitions.
* The JSON-in-a-string compromise localizes dynamic page-schema validation and
  avoids a provider-wide flag day while the current shared tool contract is
  flat. It is an explicit I2 measurement, not permission to silently flatten
  page schemas. The accepted reviewer record confirms this rationale and the
  required schema-bearing invalid-input response in
  [discussion § Reviewer round 2](discussion.md#reviewer---round-2).
* The repository mapping establishes that `ToolCallResponse.Content` is
  textual, the current adapter flattens JSON-Schema-shaped maps, and the loop
  already preserves outer call correlation. It also identifies the CLI
  composition root as the least-coupled owner for the future broker. See
  [Understanding §1](understanding.md#1-intent-and-architecture-in-my-own-words),
  [§2](understanding.md#2-verified-mapping-to-current-machinery),
  [§3](understanding.md#3-discrepancies-and-missing-prerequisites), and
  [§4](understanding.md#4-risks-and-open-questions).

The general provider-neutral raw-schema evolution remains a separate,
provider-gated contract. It MUST fail before provider dial when a legacy
projection would lose nesting, enums, unions, or unsupported assertion
semantics; it is not a prerequisite for this broker MVP. Later sections of this
C0 document freeze the lifecycle, recording, probe, annotation, and downstream
lane boundaries without changing the six-tool result contract above.

## 4. Activation, configuration, and session admission

This section freezes the operator surface for a model session. The direct
agent webmcp command group remains protocol-specific, but a session receives
browser tools only through the capability-oriented --browser-tools input.
The names and precedence below follow the accepted CLI decision and the
repository's actual Koanf nesting rule; the current Config type does not yet
contain these fields, so these are future integration edits rather than claims
about existing flags. See [Architecture Decision §2](architecture-decision.md#2-cli-and-configuration-surface)
and [Understanding §2](understanding.md#2-verified-mapping-to-current-machinery).

### 4.1 Explicit activation and precedence

The default is disabled. The following are the only activation inputs:

* browser.tools.enabled: true in YAML;
* AGENT_BROWSER__TOOLS__ENABLED=true; or
* --browser-tools=webmcp.

--browser-tools=webmcp sets both browser.tools.enabled: true and
browser.tools.backend: webmcp for that session and is a valid live-session
admission trigger by itself. A session MUST NOT return help merely because it
has no provider recording, replay capture, recording directory, or scheduled
audio turn when this flag is present. An unset flag leaves the loaded config
value in force. webmcp is the only backend value frozen by C0; an unknown
value is an invalid configuration, not a request to guess another backend.

An endpoint, user-data directory, selected browser, persisted target, replay
fixture, or direct agent webmcp command MUST NOT activate browser tools in a
model session. When disabled, the session MUST neither advertise the six
broker definitions nor dial a browser on their behalf. Direct WebMCP commands
may still perform their own explicitly requested diagnostics.

Configuration precedence is, from lowest to highest: built-in defaults, YAML,
AGENT_ environment variables, then command-line flags. An explicitly
provided selection flag wins over persisted selection state. A higher layer
replaces only the value it supplies; it MUST NOT clear unrelated browser
configuration implicitly. This matches the current defaults -> YAML -> env
loader and command-applied override model described in
[Understanding §2](understanding.md#2-verified-mapping-to-current-machinery).

### 4.2 Canonical flag, YAML, and environment mapping

The following names are normative. Duration values use Go duration syntax (for
example 30s), byte limits are non-negative decimal integers, booleans are
true or false, and list environment values are JSON arrays. Empty strings
and empty lists have the meanings shown in the table. Repeatable list flags
append values in command-line order.

| Concern | Canonical CLI flag and type | YAML key, type, and default | Environment variable | Contract meaning |
| --- | --- | --- | --- | --- |
| Activation | --browser-tools=<backend> string | browser.tools.enabled bool false; browser.tools.backend string webmcp | AGENT_BROWSER__TOOLS__ENABLED; AGENT_BROWSER__TOOLS__BACKEND | Only webmcp enables the session capability. |
| CDP HTTP endpoint | --browser-cdp-url <url> string | browser.connection.cdp_url string "" | AGENT_BROWSER__CONNECTION__CDP_URL | First explicit discovery source; credentials are rejected and query/fragment data is redacted. |
| CDP WebSocket endpoint | --browser-ws-endpoint <url> string | browser.connection.ws_endpoint string "" | AGENT_BROWSER__CONNECTION__WS_ENDPOINT | Second explicit discovery source; used only when no CDP URL is supplied. |
| Browser profile | --browser-user-data-dir <path> string | browser.connection.user_data_dir string "" | AGENT_BROWSER__CONNECTION__USER_DATA_DIR | Reads DevToolsActivePort for the selected profile; it does not imply process ownership. |
| Process discovery | --browser-allow-process-scan bool | browser.connection.allow_process_scan bool false | AGENT_BROWSER__CONNECTION__ALLOW_PROCESS_SCAN | Allows the optional, platform-specific process-discovery fallback after explicit/configured sources. It never scans arbitrary network addresses. |
| Remote CDP | --browser-allow-remote-cdp bool | browser.connection.allow_remote_cdp bool false | AGENT_BROWSER__CONNECTION__ALLOW_REMOTE_CDP | Required for a non-loopback endpoint; loopback remains the default. |
| Browser selection | --browser-browser <id> string | browser.selection.browser string "" | AGENT_BROWSER__SELECTION__BROWSER | Exact normalized browser ID; empty means no browser constraint. |
| Tab selection | --browser-tab <id> string | browser.selection.tab string "" | AGENT_BROWSER__SELECTION__TAB | Exact target ID; title and URL are never selectors. |
| Origin filter | --browser-origin <origin> string | browser.selection.origin string "" | AGENT_BROWSER__SELECTION__ORIGIN | Exact canonical origin constraint; query and fragment are not part of selection. |
| Automatic selection | --browser-auto-select=off\|single\|persisted enum | browser.selection.auto_select enum off | AGENT_BROWSER__SELECTION__AUTO_SELECT | off requires explicit selection; single permits exactly one ready match; persisted uses only a still-valid persisted ID and never falls back to another match. |
| Foreground tab | --browser-activate-tab bool | browser.selection.activate_tab bool false | AGENT_BROWSER__SELECTION__ACTIVATE_TAB | Selects without stealing focus unless true. |
| Persist selection | --browser-persist-selection bool | browser.selection.persist bool true | AGENT_BROWSER__SELECTION__PERSIST | Persists only opaque IDs and redacted metadata, never websocket credentials. |
| Allowed origins | --browser-allowed-origin <origin> repeatable | browser.policy.allowed_origins list [] | AGENT_BROWSER__POLICY__ALLOWED_ORIGINS | If non-empty, only exact origins in this list pass policy. |
| Denied origins | --browser-denied-origin <origin> repeatable | browser.policy.denied_origins list [] | AGENT_BROWSER__POLICY__DENIED_ORIGINS | Deny wins over allow and produces origin_denied. |
| Approval | --browser-approval=always\|writes\|never enum | browser.policy.approval enum writes | AGENT_BROWSER__POLICY__APPROVAL | writes requires approval for mutating or unknown tools; always includes reads; never asks for no interactive approval but does not bypass origin or input policy. |
| Interrupt cancellation | --browser-cancel-on-interrupt=never\|read-only\|always enum | browser.policy.cancel_on_interrupt enum read-only | AGENT_BROWSER__POLICY__CANCEL_ON_INTERRUPT | Controls cancellation requests, never promises rollback of a dispatched page action. |
| Invocation timeout | --browser-invocation-timeout <duration> duration | browser.limits.invocation_timeout duration 30s | AGENT_BROWSER__LIMITS__INVOCATION_TIMEOUT | Bounds local waiting and classifies an unresolved dispatched call as invocation_timed_out. |
| Input size | --browser-max-input-bytes <n> integer | browser.limits.max_input_bytes integer 262144 | AGENT_BROWSER__LIMITS__MAX_INPUT_BYTES | Bounds UTF-8 input_json bytes before page dispatch. |
| Result size | --browser-max-result-bytes <n> integer | browser.limits.max_result_bytes integer 262144 | AGENT_BROWSER__LIMITS__MAX_RESULT_BYTES | Bounds the serialized, policy-redacted result envelope; overflow is result_too_large. |
| Per-target serialization | --browser-serialize-per-target bool | browser.limits.serialize_per_target bool true | AGENT_BROWSER__LIMITS__SERIALIZE_PER_TARGET | Enforces FIFO and at most one mutating/unknown page operation per target. |
| Browser recording | --browser-record bool | browser.recording.enabled bool false | AGENT_BROWSER__RECORDING__ENABLED | Adds semantic browser events to the existing session recording bundle when recording is requested. |
| Record arguments | --browser-record-arguments bool | browser.recording.include_arguments bool true | AGENT_BROWSER__RECORDING__INCLUDE_ARGUMENTS | Retains arguments only after tool-specific redaction policy. |
| Record results | --browser-record-results bool | browser.recording.include_results bool true | AGENT_BROWSER__RECORDING__INCLUDE_RESULTS | Retains results only after size limits and redaction policy. |
| URL query redaction | --browser-redact-url-query bool | browser.recording.redact_url_query bool true | AGENT_BROWSER__RECORDING__REDACT_URL_QUERY | Removes query data from recorded URLs and diagnostics. |
| URL fragment redaction | --browser-redact-url-fragment bool | browser.recording.redact_url_fragment bool true | AGENT_BROWSER__RECORDING__REDACT_URL_FRAGMENT | Removes fragment data from recorded URLs and diagnostics. |
| Browser replay | --browser-replay <path> path | browser.replay.path string ""; browser.replay.strict bool true | AGENT_BROWSER__REPLAY__PATH; AGENT_BROWSER__REPLAY__STRICT | Selects a semantic browser fixture; strict mode rejects unknown or divergent events. Replay does not itself activate a live session. |
| Replay strictness | --browser-replay-strict bool | browser.replay.strict bool true | AGENT_BROWSER__REPLAY__STRICT | Rejects unknown or divergent semantic browser events when true. |

The equivalent default YAML shape is:

~~~
browser:
  tools:
    enabled: false
    backend: webmcp
  connection:
    cdp_url: ""
    ws_endpoint: ""
    user_data_dir: ""
    allow_process_scan: false
    allow_remote_cdp: false
  selection:
    browser: ""
    tab: ""
    origin: ""
    auto_select: off
    activate_tab: false
    persist: true
  policy:
    allowed_origins: []
    denied_origins: []
    approval: writes
    cancel_on_interrupt: read-only
  limits:
    invocation_timeout: 30s
    max_input_bytes: 262144
    max_result_bytes: 262144
    serialize_per_target: true
  recording:
    enabled: false
    include_arguments: true
    include_results: true
    redact_url_query: true
    redact_url_fragment: true
  replay:
    path: ""
    strict: true
~~~

Discovery is deterministic: explicit cdp_url, explicit ws_endpoint,
user_data_dir/DevToolsActivePort, configured values, then process discovery
only when allowed. A stale persisted selection MUST produce
stale_selection; it MUST NOT silently select a different matching tab. The
source plan's --webmcp-*, webmcp: YAML, and AGENT_WEBMCP_* names are
superseded by this table. The double underscore is required because the loader
maps each __ to one nested key, as verified in
agent-cli/internal/config/loading.go and recorded in
[Understanding §2](understanding.md#2-verified-mapping-to-current-machinery).

## 5. Cleanup and lifecycle ownership

The session capability surface is amended conceptually to:

~~~
type SessionToolCapabilities struct {
    Executor    messages.ToolExecutor
    Definitions []messages.ToolDefinition
    Close       func() error
}
~~~

Close is optional (nil means that the capability owns no closeable browser
resources), idempotent, and owned by the session coordinator after the
capability factory returns successfully. The first call performs shutdown;
later calls perform no work and return the same recorded error. A close hook
MUST never panic. If several resources fail, it MUST attempt every applicable
close step and return an errors.Join-equivalent aggregate rather than losing
later failures to an early return.

This is a contract amendment, not an edit to the current leased session file.
The need is grounded in the verified fact that SessionToolCapabilities
currently has only Executor and Definitions, while session runtime cleanup
is distributed across the runtime plan and provider/RTC paths. See
[Understanding §2](understanding.md#2-verified-mapping-to-current-machinery)
and [Architecture Decision §4](architecture-decision.md#4-overall-topology).

### 5.1 Transfer and shutdown order

Ownership transfers exactly once when the capability factory returns without an
error. Before that point, the factory/composition owner MUST close every
partially constructed browser resource on an error path. After transfer, the
session coordinator is the only caller of Close; the broker, executor,
provider adapter, and command wrappers MUST NOT close the same capability.

The coordinator performs the following ordered shutdown:

1. Stop admitting new model calls and stop scheduling a continuation.
2. Cancel pending approval prompts and queued calls. For dispatched calls,
   request browser cancellation only when cancel_on_interrupt permits it;
   cancellation is not rollback.
3. Stop catalog mutation and event admission, then let pending invocation
   waiters reach a terminal result, or classify bounded unresolved work as
   invocation_orphaned.
4. Detach every harness-attached target session. A target in an externally
   owned browser is detach-only: the harness MUST NOT close its tab, browser
   process, profile, or unrelated targets.
5. Flush and close the semantic browser recorder after the final browser event
   has been queued, then close the harness-owned browser transport/runtime.
   Only a browser launch owner holding the matching ownership token may stop a
   browser process it launched.
6. Close the provider/session runtime and flush its provider capture.
7. Finalize the existing recording bundle manifest exactly once, after both
   browser and provider artifacts are durable. A browser artifact is an
   extension of that bundle, never a second manifest contract.

The order is bounded: a non-cooperative page or transport MUST NOT hold session
shutdown forever. Any unresolved work is recorded and surfaced through the
classified result/diagnostic contract. The ordered responsibilities follow the
target-preservation, invocation-registry, and existing recording observations
in [Understanding §2](understanding.md#2-verified-mapping-to-current-machinery)
and [Architecture Decision §3](architecture-decision.md#3-testability-and-production-site-ramps).

### 5.2 One cleanup owner for each lifecycle path

The following table is normative. The session coordinator is the sole owner of
the transferred capability and the selected runtime plan for the whole run.
The runtime plan's capture finalizer, the capability's Close hook, and the
existing recording runner are delegated one-shot helpers, not competing
owners. No row may be implemented by adding a second independent defer that
closes the same resource.

| Lifecycle point or resource | Sole cleanup owner | Required behavior |
| --- | --- | --- |
| Partial browser capability construction before factory return | Capability factory/composition owner | Close all allocated browser, broker, recorder, and persisted-state handles before returning the construction error; join cleanup failures with the original error. |
| Session planning and preflight failure after transfer | Session coordinator | Close the transferred capability exactly once; do not dial the provider until browser configuration/preflight has passed. |
| injected-live runtime mode | Session coordinator | Run the injected inferencer to completion or cancellation, then invoke the common runtime-plan and capability finalizers in order. |
| replay-generic runtime mode | Session coordinator | Close replay transport/inferencer once and still invoke capability cleanup when a browser replay is paired. |
| replay-grok-websocket runtime mode | Session coordinator | Drain and close the Grok replay transport once; late browser events are recorded, not replayed into a new turn. |
| replay-openai-websocket runtime mode | Session coordinator | Preserve strict replay completion and then invoke the common runtime-plan and capability finalizers. |
| record-grok runtime mode | Session coordinator | Invoke the Grok capture finalizer once even on provider error or context cancellation. |
| record-openai runtime mode | Session coordinator | Invoke the OpenAI capture finalizer once even on provider error or context cancellation. |
| Normal session exit | Session coordinator | Invoke the common shutdown exactly once, then return the primary session result plus joined cleanup failures. |
| Error exit, including configuration, provider, browser, and tool errors | Session coordinator | Run cleanup before returning; do not skip browser cleanup because the loop already has an error. |
| Cancel/interrupt exit, including audio barge-in | Session coordinator | Stop continuation, apply the interrupt policy, reconcile or orphan pending calls, and run cleanup exactly once. |
| Duration-bounded execution and finite audio turns | Duration/session coordinator | The timer or turn boundary cancels the run and delegates to the same coordinator finalizer; it does not close provider or browser resources itself. |
| Browser semantic capture flush | Session coordinator, through SessionToolCapabilities.Close | Flush after the last browser event is queued and before its transport/runtime is closed. |
| Provider capture flush | Session coordinator, through the runtime plan's capture finalizer | Flush the provider recorder once after the runtime has stopped and before manifest finalization. |
| Recording-directory finalization | Session coordinator, through the existing recording runner | Write/update the one manifest.json once, after provider and browser artifacts, and join any finalization error with the run result. |

The explicit mode rows mirror the current runtime planner's injected, replay,
and record variants; the browser hook is common to all of them. This avoids the
current risk that duration-bounded or audio/error paths bypass a cleanup branch,
which is why the accepted architecture assigns the CLI composition root the
least-coupled browser ownership point.

No cleanup owner may terminate an external Chrome process. A process is
harness-owned only when this run launched it and retained its ownership token.
A target selected from an existing browser remains usable by the customer
after session shutdown.

## 6. Continuation and result ownership

The loop coordinator/ModelRunner exclusively owns post-tool continuation. A
browser broker is an executor and result serializer; it is never a provider
session controller. This follows the verified batch behavior (tool calls in one
batch execute concurrently and results are awaited in input order) and the
verified distinction between ordinary textual tool results and rich complete
messages. See [Understanding §2](understanding.md#2-verified-mapping-to-current-machinery)
and [Architecture Decision §4](architecture-decision.md#4-overall-topology).

For each model tool batch, the coordinator MUST:

1. register every provider call ID before dispatch;
2. allow the runner to execute calls concurrently while the broker enforces
   its own per-target FIFO/mutation policy;
3. wait until every call has one terminal result, preserving the runner's
   correlation and result ordering;
4. forward each result at most once with its original provider call ID; and
5. schedule exactly one continuation after the batch is terminal, provided the
   session is still active and no cancellation, interruption, or close has
   suppressed continuation.

The broker MUST NOT emit response.create, inject a synthetic user message,
decide that a batch is complete, or use the rich-result path. In Realtime
broker mode it returns the compact textual ToolCallResponse.Content envelope;
the provider/session loop performs the ordinary function-call-output delivery
and the one continuation through its existing provider-specific mechanism.
Whether that mechanism is direct or indirect is an implementation detail, but
there MUST be one continuation, never one per browser result and never a
second continuation from a late result.

A late browser result after model cancellation, a new user turn, tab switch,
navigation, or session shutdown is still reconciled and recorded by the broker.
It MUST NOT start a new model response. If the call ID has already received a
terminal result, a duplicate browser event is ignored for delivery and retained
only for diagnostics. A dispatched mutation whose result is unknown remains
unknown; continuation ownership does not create permission to retry it.

The source-plan claim that the browser path can simply emit output followed by
its own request is therefore superseded. The current repository has a separate
ordinary textual result path and rich-result path, and the accepted decision
requires the broker to remain on the former while the loop coordinator owns the
exactly-once continuation.
