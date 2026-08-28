## Executor - entry 1

I replaced the seed with a 118-line, repository-grounded understanding. Because the discussion file contained no reviewer entries, there was no prior feedback to address on this pass.

I organized the document around the requested machinery rather than mirroring the 29-section source plan: intent/architecture; verified mappings for the CLI tool surface, session capability construction, cleanup, timeout/cancellation, schema encoding, Realtime, recording/replay, and probes; discrepancies; risks/open questions; and a verdict for O0, C0, every lane A–Q, and gates I1/I2. Each codebase assertion is accompanied by one or more paths I read, and I explicitly separated unverified external dependency/browser claims from repository facts.

The most material corrections are: raw schema affects Anthropic/Gemini/Grok and copy paths beyond the plan's OpenAI-only lane; `SessionToolCapabilities` has no cleanup hook; ordinary OpenAI Realtime tool-result encoding currently does not append `response.create`; audio barge-in response cancellation is separate from loop/tool interruption; the loop executes tool batches concurrently; the existing recording bundle names differ from the proposed bundle; and the source plan's browser probe example is rejected by the current strict scenario grammar. I treated those as contract-freeze or gate blockers rather than implying existing machinery already solves them.

Open questions for review are whether the raw-schema lane should explicitly require all provider encoders to understand `InputSchema` versus retaining a legacy projection, and whether paired browser evidence should extend the existing transcript manifest or introduce a separately versioned parent manifest. I recommend freezing both answers in C0. I also recommend freezing a single owner for post-tool `response.create` and for capability/broker cleanup before parallel lanes begin.

## Reviewer - round 1

I independently spot-checked the understanding document against the repository and the source plan, sampling citations from every section rather than trusting the executor's summary. Result: every check passed. Assessment and evidence follow.

**Verified accurate (sampled independently):**
- Workspace/toolchain: `go.work` and all three module manifests are Go 1.24.2; no `chromedp`/`cdproto` anywhere in `agent-cli/go.mod`, `go-agent-loop/go.mod`, `go-llm-gateway/go.mod`, or `go.work.sum`. Helper modules (`test/localai/go.mod`, `tools/coveragegate`, `tools/rtc-race-gate`, `tools/session-race-gate`, `tools/timingate`) all exist as cited.
- Routing: `agent-cli/internal/cli/routes.go` has exactly the ask/chat/tool/interaction/probe/media/session/config/devices groups and no WebMCP route.
- Config: `Config` in `agent-cli/internal/config/interface.go` has only `Model` and `Tools`; `loading.go` confirms `AGENT_` prefix with `__` nesting.
- Session capability: `SessionToolCapabilities` (`agent-cli/internal/cli/session.go:26-29`) has only `Executor` and `Definitions`. The plan's proposed `Close func() error` (source-plan.md ~line 2035) genuinely does not exist — the document's framing as "design, not current machinery" is correct.
- Live-mode admission: `session.go:382-383` returns `cmd.Help()` unless record/replay/record-dir/audio-in-turn is set. The document's discrepancy #9 about the plan's `agent session --webmcp` example (plan line 197) is real and non-obvious — good catch.
- Timeout/cancellation: `defaultSessionToolExecutionTimeout = 60 * time.Second` in `session_tools.go`; `ToolRunner` runs batch calls concurrently (`tool_runner.go:202`); `InterruptHandler` cancels model and tool per-execution contexts; `model_runner.go` sends RESPONSE.CANCEL on barge-in as a separate path. All match the document's characterization, including the claim that no browser-aware bridge exists among them.
- Schema flattening: `ToolDefinition` carries only flat `[]ToolParameter` (`tool_values.go:13-25`); the CLI adapter flattens `properties` (`adapter.go:101-103`); Anthropic/Gemini/OpenAI all rebuild from `.Parameters`; Grok passes `config.Tools` directly into `session.update`. The document's "blast radius wider than the plan says" correction is verified, not asserted.
- Realtime continuation: `session_events.go:198-200` deliberately appends no `response.create` after `function_call_output`; `model_runner.go` `sendLatestUserText`/`sendLatestSessionToolResults` provide the indirect continuation; the rich complete-message helper requests the response directly (`session_messages.go:25`, confirmed by `composed_tool_result_test.go`). This is the subtlest claim in the document and it is exactly right.
- Recording/replay: `manifest.json` and versioned `RecordingManifest` in `go-agent-loop/pkg/transcript/manifest.go`; `openAIReplayToolDefinitions` in `session_runtime_openai.go` conditionally reuses definitions only for captures whose `session.update` has a `tools` field — matches the document.
- Probe grammar: `scenario.go` allows only send_text/send_audio/send_tool_result/advance_to/wait/close; top-level fields limited to id/name/description/steps/expectations(+aliases); `send_audio` takes `corpus_id`, not a path. The plan's 16.4 example (top-level `browser_fixture`, `provider_fixture`, `expect`, `send_audio` with `"path"`) is indeed invalid today — discrepancy #6 and the Lane K caveat are correct.
- Lane structure: plan sections 1A/19 define O0/C0/Lanes A–Q/Gates I1-I2 as the document maps them; CI (`.github/workflows/ci.yml`) contains no Chrome setup.

**Requirements check:** all five requested parts are present; 121 lines (under 250); external Chrome/chromedp/WebMCP-spec claims are honestly quarantined as operator gates rather than laundered as verified; the per-lane table gives a grounded reason with real paths for every lane. This is an audit of the plan against the code, not a summary of the plan's own claims.

**Nits (no change required):** your discussion entry says "118-line" but the file is 121 lines — trivial counting drift, immaterial. 

**Replies to your open questions:** both are correctly framed as C0 freeze decisions and the understanding document is the wrong place to resolve them; recording them as open questions with the affected provider/manifest paths cited is exactly what the task asked for. For what it's worth: a legacy projection that fails loudly on unions/nesting (rather than silently weakening) plus raw-schema support gated per provider capability seems most consistent with the codebase's existing strictness (cf. the strict scenario loader and strict replay divergence reporting), and extending the existing versioned `RecordingManifest` rather than a second parent manifest matches how provider capture and transcript bundle already pair — but freeze either answer in C0, not here.

**Decision: ACCEPTED.** The document is accurate, complete, honestly caveated, and grounded in files I independently confirmed.

## Executor - entry 2

I replaced the seed with a 270-line decision document and deliberately did not
rubber-stamp two prominent source-plan choices. The largest amendment is to
decouple WebMCP broker delivery from raw-schema migration. The MVP broker can
stay inside today's flat contract if `webmcp_invoke` takes a required opaque
`tool_ref` plus `input_json string`; page schemas remain data returned by
`webmcp_list_tools`, not provider tool definitions. This avoids a cross-provider
flag day and removes raw schema from I1/I2, at the admitted cost of JSON-in-a-
string model ergonomics. I made successful Realtime use of that representation
an I2 measurement rather than assuming it works.

For the general codebase, I rejected flat parameters as a permanent rule. I
adopted the direction suggested in the reviewer's round-1 reply: lossless raw
schema as a separate shared capability, explicit per-provider/per-mode gating,
and projection only when every constraint is exactly representable by legacy
`ToolParameter`. Any enum, union, nesting, or unknown keyword that would be
dropped must fail before provider dial. I also added the missing ambiguity rule:
when raw and legacy forms coexist they must agree under strict projection.
OpenAI may implement raw support first; Anthropic, Gemini, and Grok need their
own dialect validation and wire goldens before claiming it.

On CLI naming I chose a hybrid rather than a blanket rename. `agent webmcp`
remains precise for direct protocol diagnostics and fits the existing top-level
domain groups. Session activation becomes `--browser-tools=webmcp`, with
`--browser-*` operational flags and nested `browser.tools` configuration. This
keeps future backend room without claiming generic UI control. I explicitly
rejected `--browser-controls` as too broad, and corrected environment spelling
to the loader's actual double-underscore nesting convention
(`AGENT_BROWSER__TOOLS__ENABLED`, etc.). Endpoint configuration alone cannot
activate tools; an explicit flag or enabled setting can, and that activation is
a live-session admission trigger.

The testing decision makes fakes, paired semantic/provider replay, and a local
fixture in pinned Chrome complementary rather than substitutes. The production
greeting-card, artwork, and showcase sites move to supervised, dated
compatibility/demo canaries after hermetic gates. They can block a claim that a
particular demo/site works, but do not become presubmit correctness oracles.

I retained the in-process CLI broker after contesting direct dynamic projection,
page-as-remote-MCP, broker-as-remote-MCP, sidecar, extension, direct session
coupling, and generic UI automation. The alternatives have legitimate future
advantages, but the broker is the only option that simultaneously owns the
local selected-tab lifecycle and approval boundary, keeps stable Realtime
definitions, and provides a deterministic seam. The document makes it a
conformance boundary so a sidecar, extension, projection mode, or remote facade
can be added without replacing policy/catalog semantics.

Please stress three points in particular: whether JSON-string arguments are an
acceptable deliberate MVP compromise for Realtime rather than merely moving
schema complexity into prompting; whether retaining `agent webmcp` while using
`--browser-tools=webmcp` creates harmful naming duality; and whether production
site success should block a contest/demo release gate even though it must not
block hermetic merge gates. I would push back on requiring raw-schema migration
solely to make the broker input native: that couples the most volatile provider
surface to a path whose central benefit is stable indirection. I would amend if
evidence shows the target Realtime model cannot reliably emit `input_json`.

## Reviewer - round 2

I stress-tested the decision document independently rather than re-checking only
what round 1 already verified. New citations sampled this round, all confirmed:
`go-llm-gateway/pkg/capabilities/capabilities.go` really does define a
per-provider, per-mode (stateless vs session) feature vocabulary with explicit
unknown/supported/unsupported states and a pre-dial `UnsupportedFeatureError` —
so "extend the existing capability vocabulary with a tool-schema capability and
fail before provider dial" is grounded in existing machinery, not invented;
`agent-cli/internal/wire/composition.go` (~lines 119–131) does replace the
registry executor with a config-filtered registry (`NewToolRegistryFromConfig`)
at the composition root, supporting the claim that the CLI composition root is
the least-coupled broker owner; `agent-cli/internal/services/session_recording.go`
exists as cited; OpenAI `session_config.go` rebuilds `parameters` from the flat
list as claimed. I also confirmed every amendment targets real source-plan
content: the `oneOf` `tool_ref|tool_name` definition at 10.5 (lines ~1719–1748),
the `--webmcp*` flag family (lines ~1925–1944), single-underscore
`AGENT_WEBMCP_*` names (lines ~1951–1958) that genuinely cannot encode the
loader's `__` nesting, and Lane E's raw-schema-on-the-critical-path structure
(line ~3304, ~4521). The document is 270 lines, under the 300 cap.

**Coverage against the operator's four questions:** all mandated alternatives
are genuinely contested, not strawmanned. Q1 evaluates exactly the three
required routes plus two more (permanent flat rule, typed AST) and separately
answers the general-rule question with a firm rejection of flat-forever. Q2
evaluates `--browser-controls`, a browser route group, and capability naming,
fixes the env spelling, and preserves future non-WebMCP room via the
`--browser-tools=<backend>` value form. Q3 places all five mechanisms in a
six-rung ladder with an explicit, defensible home for the production
greeting-card/artwork ramps. Q4 contests eight topologies including three the
request didn't name. Each verdict has a stated decisive reason.

**Stress results on the three points you asked me to push on:**

1. *JSON-string arguments.* Acceptable as a deliberate MVP compromise, and I
   verified the document does not merely assert it: it makes Realtime success an
   explicit I2 measurement with a named fallback (accelerate the independent
   raw-schema lane and the deferred projected mode), and the broker decodes and
   validates before Chrome sees anything. Generic-dispatch tools with
   string-encoded payloads are a well-precedented pattern, and the alternative —
   coupling the MVP to a cross-provider shared-contract change whose blast
   radius round 1 confirmed spans Anthropic/Gemini/Grok/clone/replay paths — is
   strictly worse for schedule risk. One non-blocking strengthening suggestion
   for implementation, not the plan: on `input_json` validation failure the
   broker should return the selected tool's schema in the error result so the
   model can self-correct in-session.
2. *Naming duality.* Not harmful. `agent webmcp` names the protocol you are
   diagnosing (honest, searchable, parallel to `devices` in `routes.go`);
   `--browser-tools=webmcp` names the capability you are granting a session.
   Those are different speech acts and the split is the correct resolution of
   the operator's "best of all evils" worry; a blanket rename either way would
   have been worse.
3. *Production-site gating.* Agreed as written: production runs block the claim
   "this demo/site works" (the Demo compatibility gate) and never block hermetic
   merges without fixture reproduction. That answers the operator's ramp
   definition directly instead of dodging it — the ramps survive, but as dated
   supervised canaries with safe data, not as correctness oracles over schemas
   the sites can change without notice.

**Watch item, non-blocking:** the "no encoder may silently drop an unknown
keyword" rule is maximally strict; benign annotation keywords (`title`,
`examples`, `$schema`) in real page schemas could make raw mode fail loudly and
often. This is confined to the independent raw-schema lane (broker mode carries
schemas as data, so the MVP is unaffected), it is consistent with the codebase's
existing strictness (strict scenario loader, strict replay divergence), and the
per-provider dialect validators are the right place to decide an allowlist —
freeze that list in C0.

**Defects found: none material.** The document is internally consistent (the
decision summary, section verdicts, amendments, and gates all agree), every
recommendation is justified against its alternatives with repository evidence I
sampled myself, prior round-1 feedback was genuinely incorporated (provider
blast radius, capability-gated raw schema with fail-loud projection, extending
the existing versioned manifest rather than a parallel bundle, the invalid probe
example), and the amendments section makes the changes to `source-plan.md`
concrete and bounded.

**Decision: ACCEPTED.**
