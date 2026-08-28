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
