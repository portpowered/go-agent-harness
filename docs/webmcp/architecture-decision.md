# WebMCP browser control: architecture decisions

**Status:** executor proposal for independent review  
**Scope:** amendments to `source-plan.md`; repository state was checked against
`understanding.md`. This document decides architecture, not implementation pins
that still require the source plan's operator probes.

## Decision summary

1. Keep the in-process, CLI-owned broker as the primary architecture.
2. Make the MVP broker definitions representable by today's flat tool contract;
   page schemas travel as tool-result data, not provider tool definitions.
3. Treat lossless raw JSON Schema as a valuable, separate shared-contract
   evolution. Add it incrementally with provider capability gating and a
   fail-loud legacy projection; do not make it a WebMCP MVP prerequisite.
4. Keep `agent webmcp` for protocol-specific direct operations, but replace the
   session's bare `--webmcp` switch and flag family with the capability-oriented
   `--browser-tools=webmcp` surface and nested `browser.tools` configuration.
5. Make hermetic fixtures and replay the regression authority. Production
   greeting-card/artwork sites are dated compatibility/demo canaries, never
   deterministic correctness tests.

## 1. Tool schema contract

### Facts that constrain the choice

- `messages.ToolDefinition` can express only `[]ToolParameter`; each parameter
  has a name, primitive-ish type, description, and required bit
  (`go-agent-loop/pkg/messages/tool_values.go`).
- The CLI takes a JSON-Schema-shaped map and discards everything except root
  properties and required flags (`agent-cli/internal/tools/adapter.go`).
- OpenAI chat and Realtime rebuild shallow object schemas
  (`go-llm-gateway/pkg/providers/openai/models.go`,
  `go-llm-gateway/pkg/providers/openai/session_config.go`). Anthropic and Gemini
  independently rebuild from the same flat list, while Grok serializes shared
  session definitions directly (`go-llm-gateway/pkg/providers/anthropic/models.go`,
  `go-llm-gateway/pkg/providers/gemini/models.go`,
  `go-llm-gateway/pkg/providers/grok/provider.go`). A shared-field change is
  therefore not an OpenAI-only change.

### Alternatives contested

| Alternative | Benefits | Costs / failure modes | Verdict |
|---|---|---|---|
| Keep flat definitions and expose only simple broker arguments | Smallest MVP; no provider migration; stable definitions; page schema changes do not require `session.update` | Model must pass escaped JSON text; discovery/invoke takes two calls; broker must parse and validate | **Use for broker MVP** |
| Add raw schema, gate by provider, strictly project to legacy where possible | Lossless general contract; incremental adoption; unsupported shapes fail before a provider call | Two representations and capability tests; copy/clone/replay work; providers support different schema subsets | **Adopt independently** |
| Full raw-schema cutover now | One authoritative representation and best future tool fidelity | Cross-provider flag day; risks breaking existing static tools and Grok wire shape; makes unrelated work block WebMCP | Reject for MVP |
| Keep flat parameters as the permanent general rule | Very simple encoders | Cannot faithfully express nested objects, arrays, enums, unions, nullable values, or nested required fields | Reject |
| Build a repository-owned typed JSON Schema AST | Compile-time manipulation and validation | Large standards surface, version drift, and provider dialect translation; less tractable than preserving bytes | Reject |

### Recommendation

The stable broker tools must use only the subset the current contract preserves.
In particular, replace the source plan's nested/`oneOf` definition of
`webmcp_invoke` with required scalar fields:

```text
webmcp_invoke(tool_ref string, input_json string, reason string)
```

`tool_ref` must come from `webmcp_list_tools`; the model-facing call does not
accept the ambiguous `tool_name` alternative. `input_json` is a JSON object
encoded as a string. The broker decodes it, enforces size/policy, optionally
validates it against the selected page descriptor, and passes raw object bytes
to Chrome. Direct CLI may retain unique-name convenience because it can fail
interactively. `webmcp_list_tools` returns each page schema as JSON result data;
that schema never enters the Realtime function-definition array in broker mode.

This JSON-in-string boundary is less ergonomic than a native object, but it is
localized, testable, stable across navigation, and avoids pretending the flat
contract preserved a schema. Realtime model success with it is an explicit
Gate I2 measurement; failure justifies accelerating raw support, not silently
weakening a page schema.

Separately, add `InputSchema json.RawMessage` to the provider-neutral definition
as the eventual authoritative representation. Keep `Parameters` deprecated for
compatibility. Constructors must reject malformed/non-object schemas and reject
both fields when a strict projection of `InputSchema` does not equal
`Parameters`. Clone raw bytes in inferencer, snapshot, and replay paths.

Extend the existing provider capability vocabulary
(`go-llm-gateway/pkg/capabilities/capabilities.go`) with an explicit tool-schema
capability for stateless and session modes. At composition, each encoder must:

1. use raw schema only when that provider/mode claims support and its dialect
   validator accepts the keywords;
2. otherwise project only schemas whose information is exactly expressible as
   `ToolParameter`;
3. fail before provider dial with provider, mode, tool name, and unsupported
   schema location when projection would lose information.

No encoder may silently drop a union, enum, nested constraint, or unknown
keyword. OpenAI can be the first raw-capable implementation. Anthropic, Gemini,
and Grok become raw-capable only with their own wire goldens. Flat parameters
remain a convenience/compatibility format, not the general rule for new tools.

## 2. CLI and configuration surface

### Alternatives contested

| Surface | Benefits | Costs / ambiguity | Verdict |
|---|---|---|---|
| `agent webmcp ...`; `agent session --webmcp` | Precise, searchable, challenge-friendly | Session API is tied to one backend; every option gets a protocol prefix | Split decision |
| `agent browser ...`; `--browser-controls` | Friendly and future-looking | Overclaims screenshot/DOM/computer control; “controls” obscures read vs mutation and backend | Reject now |
| `agent browser webmcp ...` | Clean namespace if several browser families exist | Premature two-level hierarchy and verbose commands for the only current family | Defer |
| Capability activation: `--browser-tools=webmcp` | Says what the model receives and leaves room for another backend | Slightly longer; user must know the backend value | **Use for sessions** |

### Recommendation

Retain `agent webmcp {doctor,browsers,tabs,select,activate,context,tools,invoke,
cancel,watch}`. These commands inspect and invoke the WebMCP protocol itself, so
the specific name is honest. It also fits the top-level noun/domain groups in
`agent-cli/internal/cli/routes.go` (`interaction`, `probe`, `media`, `session`,
`config`, `devices`). Do not add an alias or an empty `agent browser` group yet;
add `agent browser <backend>` only when a second real family shares useful UX.

For model sessions use:

```text
agent session --browser-tools=webmcp [--browser-cdp-url ...] [--browser-tab ...]
```

Use `--browser-*` for connection, selection, policy, timeout, recording, and
replay options. The value form permits a later backend without changing the
meaning of “make browser tools available to this agent.” `--browser-controls`
is rejected because the exposed capability includes reads and semantic page
tools, not generic UI control.

Add a `Browser` section to `config.Config`
(`agent-cli/internal/config/interface.go`) with this conceptual shape:

```yaml
browser:
  tools:
    enabled: false
    backend: webmcp
  connection:
    cdp_url: ""
    user_data_dir: ""
  selection:
    browser: ""
    tab: ""
  policy:
    approval: writes
```

Environment spelling must follow the actual Koanf nesting rule in
`agent-cli/internal/config/loading.go`: for example
`AGENT_BROWSER__TOOLS__ENABLED`, `AGENT_BROWSER__TOOLS__BACKEND`,
`AGENT_BROWSER__CONNECTION__CDP_URL`, and
`AGENT_BROWSER__POLICY__APPROVAL`. The source plan's single-underscore
`AGENT_WEBMCP_CDP_URL` examples do not encode nesting and must be removed.
Endpoint configuration alone never enables tools. An explicit CLI value or
`browser.tools.enabled: true` does. Browser-tool activation must count as a
valid live-session trigger in `agent-cli/internal/cli/session.go`; recording is
recommended but not required.

## 3. Testability and production-site ramps

### Alternatives contested

| Mechanism | What it proves | What it cannot prove |
|---|---|---|
| Contract fakes/fake clock | Broker state, races, cancellation, policy, classified failures | Compatibility with generated CDP bindings or Chrome |
| Strict semantic browser recordings | Deterministic catalog/invocation/recovery sequences and readable diffs | Current browser behavior; a recording can preserve an old misunderstanding |
| Hermetic local fixture in pinned Chrome | Actual WebMCP enable/events/commands, navigation, removal, object results, independent page-state oracle | Diversity and drift of real sites |
| Provider replay and finite audio | Exactly-once tool/result/continuation and voice choreography | Live provider/model variance |
| Production sites | Ecosystem compatibility and demo/product realism | Stable schemas, network independence, safe mutations, reliable oracle |

### Recommended testing ladder

1. **Every change:** pure broker/catalog/invocation/policy unit tests using fake
   clock/IDs; schema parser/projection goldens; CLI/config/combined-executor
   contract tests.
2. **Every change:** generated-command/event adapter tests plus strict browser
   semantic replay. Pair browser replay with existing provider capture/replay;
   reference both from a versioned extension of the current `manifest.json`,
   not a second competing bundle (`go-agent-loop/pkg/transcript/manifest.go`,
   `agent-cli/internal/services/session_recording.go`).
3. **Browser integration CI:** pinned Chrome for Testing against a hermetic page
   served locally. Cover list/invoke/object/error/cancel/navigation/removal,
   listener-before-enable, target preservation, and an out-of-band state oracle.
4. **Session integration:** the same fixture with Realtime text replay/live fake,
   then finite recorded audio; prove call-ID correlation, exactly one
   continuation, interruption, and cleanup using existing session seams.
5. **Human/local gate:** live provider plus fixture, first text and then
   microphone/speaker, with paired provider/browser evidence.
6. **Compatibility/demo canary:** greeting-card and artwork-design production
   sites, plus chosen showcase sites, only after the fixture gates pass.

Production-site ramps belong in a scheduled/manual compatibility matrix and in
demo readiness, not in required presubmit CI and not as the oracle for broker
correctness. Each run records date, URL/origin, browser/provider pins, discovered
catalog digest, exact script, permissions/account kind, and redacted evidence.
Use read-only operations or disposable data. Never hard-code their current tool
names/schemas as goldens. An external failure blocks a claimed demo/site
compatibility result, but blocks a merge only after reproduction against the
fixture or evidence of a protocol regression. The strict grammar in
`go-agent-loop/pkg/probe/scenario.go` must be versioned/extended before browser
steps are used; the source plan's currently invalid example is not a fixture.

## 4. Overall topology

### Alternatives contested

| Architecture | Strong case for it | Decisive weakness for the MVP | Verdict |
|---|---|---|---|
| CLI-owned in-process broker with stable model tools | Stable session contract, local tab/policy ownership, deterministic fake seam, reuses session executor | One extra discovery call and JSON-string input | **Primary** |
| Directly project page tools into Realtime | Best model affordance; fewer model calls; native schemas | Dynamic `session.update`, removal/atomicity, naming collisions, stale calls, raw-schema/provider/replay coupling | Deferred mode behind broker |
| Treat page as remote MCP server | Realtime-native remote tool topology | A selected local tab is not a remotely reachable MCP server; provider-side execution bypasses local selection/approval/lifecycle | Reject |
| Expose broker itself as remote MCP | Reusable by remote agents | Requires a reachable authenticated service and moves sensitive browser authority across a network boundary | Later separate product |
| External sidecar | Process isolation; independent/browser-ecosystem release cadence | Packaging, IPC, health, version skew, cleanup, and credential/endpoint exposure | Optional `BrowserRuntime` backend later |
| Browser extension | User-approved active-tab UX and permissions | Distribution/signing, browser-specific messaging, new lifecycle and test surface | Optional attach backend later |
| Put Chrome calls directly in session/provider code | Short initial path | Couples `agent-cli`, loop timing, and provider wire code; duplicates policy/replay; hard to test independently | Reject |
| Generic pixel/DOM computer control | Works without page tools | Less semantic, brittle, harder to authorize and verify; does not validate WebMCP | Out of scope |

### Recommendation

Keep the broker in `agent-cli/internal/webmcp`, behind neutral browser runtime,
catalog, policy, recorder, and clock interfaces. `go-agent-loop` owns generic
execution/correlation; `go-llm-gateway` owns provider encoding; neither imports
browser concepts. The current session already composes matching executor and
definitions through `SessionToolCapabilities` (`agent-cli/internal/cli/session.go`)
and production creates a config-filtered tool registry
(`agent-cli/internal/wire/composition.go`), making the CLI composition root the
least-coupled owner. Add the planned cleanup hook and combined executor there.

The broker is also the migration seam: projected tools, a sidecar, an extension,
or a remote-MCP facade must implement/use the same broker/runtime contracts and
pass its conformance suite. Projection may become `projected`/`hybrid` only
after atomic dynamic updates, provider schema capability, naming, stale-call,
and replay behavior are proven; broker tools remain the recovery path.

## Concrete amendments to `source-plan.md`

1. Revise the executive decisions to distinguish **broker MVP schema** from
   **general raw-schema evolution**; remove raw schema from the I1/I2 critical
   path and from claims required for broker completeness.
2. Rewrite Sections 9 and 10.5 with scalar `tool_ref`, `input_json`, and `reason`;
   page schema is list-result data. Replace Lane E with an independent,
   provider-gated raw-schema lane and add fail-loud projection/goldens for
   OpenAI, Anthropic, Gemini, Grok, clone paths, and replay.
3. Keep the Section 11 direct `agent webmcp` group. Replace session `--webmcp*`
   flags with `--browser-tools=webmcp` and `--browser-*`; replace `webmcp:` config
   and `AGENT_WEBMCP_*` examples with nested `browser:` config and double-
   underscore `AGENT_BROWSER__...` names. Make activation a live-mode trigger.
4. Rewrite testing sections/gates to use the six-rung ladder above. Make local
   fixture plus pinned Chrome the real-browser regression authority; move
   production greeting-card/artwork/showcase runs to non-hermetic compatibility
   and demo gates with dated evidence and safe data.
5. Retain CLI-owned broker, local function tools, neutral Chrome adapter, target
   ownership, preflight, cleanup, approval, recording, and cancellation design.
   Reclassify projection, remote-MCP facade, sidecar, and extension as explicit
   post-broker alternatives that must pass broker conformance.
6. Correct the probe example and extend the existing recording manifest instead
   of introducing the parallel artifact layout noted in the accepted
   understanding.

## Decision gates

- **C0:** freeze scalar broker definitions, CLI/config names, cleanup ownership,
  errors, semantic recording version, and strict fixture schema.
- **I1:** hermetic direct-CLI fake/replay and pinned-Chrome fixture pass; customer
  tab survives detach; no provider or production site is required.
- **I2:** fixture-backed Realtime text and finite/live audio pass, including
  JSON-string invocation, exactly-one continuation, interruption, objective
  state, paired evidence, and cleanup.
- **Demo compatibility:** selected production sites pass dated, supervised
  scripts. This supports the demo claim but is not a deterministic regression
  gate.
