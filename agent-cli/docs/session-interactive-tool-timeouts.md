# Interactive voice tool latency contract

---
owner: Agent CLI maintainers
last modified: 2026, August, 29
---

This document is the maintainer catalog for tool calls made by `agent session`.
It applies to voice/realtime sessions, including live record and credential-free
replay. The session planner resolves one immutable policy with the exact tool
definition snapshot that it advertises to the provider; the executor uses that
same snapshot when it chooses a deadline.

## Policy and configuration

The defaults are deliberately separate from ordinary ask, chat, cron, and batch
tool execution:

| Setting | Default | Valid range | Environment override |
| --- | --- | --- | --- |
| `tools.interactive.fast_read_timeout` | `5s` | greater than `0` and less than `10s` | `AGENT_TOOLS__INTERACTIVE__FAST_READ_TIMEOUT` |
| `tools.interactive.long_running_timeout` | `20s` | greater than `0` and less than `30s`; must exceed the acknowledgement threshold | `AGENT_TOOLS__INTERACTIVE__LONG_RUNNING_TIMEOUT` |
| `tools.interactive.acknowledgement_threshold` | `2s` | greater than `0` and no greater than `2s` | `AGENT_TOOLS__INTERACTIVE__ACKNOWLEDGEMENT_THRESHOLD` |

The equivalent YAML is:

```yaml
tools:
  interactive:
    fast_read_timeout: 5s
    long_running_timeout: 20s
    acknowledgement_threshold: 2s
```

All durations must be Go duration strings. Invalid or non-positive values are
rejected while configuration is resolved, before a provider or tool side
effect begins. An unknown or newly added tool is intentionally classified as
`fast/read` until it is explicitly admitted as long-running.

The interactive policy does not change non-interactive behavior. `agent ask`,
`agent chat`, cron execution, and direct batch tool adapters retain their
existing configured deadlines. The legacy session wrapper used by callers that
do not supply an interactive policy also retains its `60s`
`defaultSessionToolExecutionTimeout` compatibility bound.

## Built-in session tool catalog

The static registry currently contains these built-in tools. A tool listed as
`fast/read` gets `fast_read_timeout`, even when its operation is a write or
mutation; the class is a latency class, not a permission class.

| Tool | Class | Admission or routing note |
| --- | --- | --- |
| `read_file` | fast/read | Static tool; enabled by the tool list. |
| `read_image` | fast/read | Static tool; enabled by the tool list. |
| `write_file` | fast/read | Static tool; enabled by the tool list. |
| `edit_file` | fast/read | Static tool; enabled by the tool list. |
| `append_file` | fast/read | Static tool; enabled by the tool list. |
| `list_dir` | fast/read | Static tool; enabled by the tool list. |
| `web_fetch` | fast/read | Static tool; enabled by the tool list. |
| `web_search` | fast/read | Static tool; enabled by the tool list. |
| `show` | fast/read | Display-dependent; advertised only after a usable capture surface is proven. |
| `mouse` | fast/read | Display-dependent companion; omitted with `show` when the surface is unavailable. |
| `load_skill` | fast/read | Static tool; enabled by the tool list. |
| `exec` | bounded-long-running | The explicit shell-work exception; eligible for acknowledgement while pending. |
| `sleep` | bounded-long-running | The deterministic long-running test and wait tool; eligible for acknowledgement while pending. |

When browser tools are enabled, the six stable WebMCP tools
`webmcp_get_context`, `webmcp_list_tabs`, `webmcp_select_tab`,
`webmcp_list_tools`, `webmcp_invoke`, and `webmcp_cancel` are also eligible.
They are `fast/read` by default. Page-defined WebMCP tools discovered after
browser initialization are likewise `fast/read` unless their name is an
explicitly admitted long-running class. Browser admission and the final
definition snapshot happen before the provider receives the session
configuration.

### Display admission rule

`show` and `mouse` are session capabilities, not unconditional defaults. The
resolver performs a bounded, side-effect-free display/capture probe before
the final provider tool definitions are sent. A headless or inconclusive probe
fails closed: the definitions and matching executor routes are omitted. The
resolver never invents display dimensions or captures a frame just to make
admission succeed.

If a previously admitted display surface disappears, `show` returns one
correlated unavailable result within the fast/read budget. Capture subprocesses
must be context-bound, so cancellation and deadline expiry are observable as a
tool result rather than a session hang.

## Timing origins and observable boundaries

Measure all latency from monotonic timestamps in the same process when possible.
The runtime observer and recording stream expose the following boundaries:

| Boundary | Origin and observable event |
| --- | --- |
| Input commit | `input_commit` is emitted after the session successfully sends the client `MESSAGE.END` for the input. For audio, it is the end of the PCM accumulated since the previous commit. This is the start of the dead-air measurement. |
| Tool deadline | The per-call child context starts immediately before the composed executor is invoked. It is `fast_read_timeout` or `long_running_timeout` from the session snapshot; expiry cancels only that child context, not the enclosing session. |
| Provider-visible failure | The executor returns a correlated result retaining the original call ID and tool name. The tool-result stream then emits the provider-facing tool result and the continuation request. For timeout, the stable classification is `interactive_tool_timeout`. |
| Acknowledgement audio | A long-running call still pending at `acknowledgement_threshold` schedules one response with acknowledgement purpose. The first non-empty `audio_output` observation for that response is the observable acknowledgement boundary; it is not a grounded turn and does not close the tool call. |
| Final continuation | After the original result is accepted, exactly one ordinary response is requested. Its non-empty assistant text/audio and terminal message are the grounded continuation boundary. A timeout or other failure follows the same correlated-result path. |
| Dead-air limit | The customer bar is measured from `input_commit` to first non-empty assistant `audio_output`; it must stay below `30s`. Session duration, transport-close, and recording-finalization timestamps are not substitutes for first assistant audio. |

Tool results are internal loop messages until the provider boundary accepts
them. Do not call a local executor return, a tool delta, or an acknowledgement
request “assistant output.” For live evidence, record the input-commit time,
tool-result acceptance time, first acknowledgement audio if any, first final
assistant audio, and the terminal verdict separately.

## Hermetic regression gate

The required regression evidence is credential-free and deterministic. Keep the
stalled-executor, timeout/correlation, continuation, acknowledgement, display
admission, cancellation, and parallel-call tests in the normal hermetic test
suite. They use injected executors, display surfaces, clocks, and process
boundaries; they must not require a desktop, provider credentials, or a live
network service. Live availability and provider timing are supplemental
evidence, never a CI dependency.

## One live confirmation

Run exactly one short confirmation against the final pushed binary when a
test-account credential is available. Use the cost-bounded mini realtime model,
keep the prompt terse, bound the session at `30s`, and write the capture outside
the checkout. The prompt asks the model to use `show` when it is advertised and
to report the honest unavailable result otherwise, so the run covers either the
repaired display-admission path or its correlated failure path.

```bash
make build

export OPENAI_API_KEY="<loaded-from-a-private-secret-store>"
trap 'unset OPENAI_API_KEY' EXIT

./agent-cli/bin/agent session \
  "Take a screenshot and say whether it was available." \
  --provider openai \
  --model gpt-realtime-2.1-mini \
  --api-key "$OPENAI_API_KEY" \
  --system-prompt "You are a terse realtime voice assistant. Use show if it is advertised. If unavailable, say so in five words or fewer. Do not use another tool." \
  --record /tmp/s2s-interactive-tool-timeout-budget.session.json \
  --max-duration 30s
```

The PR conversation, rather than the repository, must record the exact
secret-free command, provider/model, tested commit, effective interactive
configuration, `input_commit`, tool-result, first-audio, and terminal times,
the pass/fail verdict, and a sanitized capture path or summary. Never attach
raw customer audio, authorization headers, API keys, session IDs, or an
unsanitized provider capture. If no credential is available, report the live
check as blocked; do not convert a skipped provider call into a passing
confirmation.
