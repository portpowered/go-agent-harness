# s2s vertical v4e — unregistered tool names yield a typed refusal

Status: **proven in-repo** (2026-08-26) by a hermetic, CLI-driven integration
lane with no network and no credentials.

## What is proven

`TestUnknownToolRefusalThroughBuiltCLI`
(`agent-cli/test/integration/tool_unknown_test.go`) launches real `agent-cli`
processes as child processes with their production argv over the committed,
replay-only capture
`agent-cli/test/integration/testdata/s2s-v4e-tool-unknown.session.json`. No
Cobra constructor, session service, probe executor, or tool-routing function
runs in-process. The proof has three observations:

1. **Unregistered-name condition.** `agent --config-dir <dir> tool --list`
   observes the active session registry through the public CLI: `read_file`
   is present and `s2s_v4e_unregistered_tool` — the name the replay requests —
   is absent. The refusal condition is anchored to the actual registry, not to
   a name the test merely declares unknown.
2. **Typed refusal observable.** `agent --config-dir <dir> session --replay
   <fixture>` prints the stable machine-readable refusal payload

   ```
   {"type":"refusal","classification":"unknown_tool","tool_name":"s2s_v4e_unregistered_tool"}
   ```

   carrying the exact requested name. A generic error string alone would not
   satisfy the assertion: the test decodes `type`, `classification`, and
   `tool_name` from the projected output.
3. **Post-refusal completion signal.** The same session output records the
   close boundary (`[session closed: fixture_complete]` plus a
   `[session terminal: ...]` line), the process exits 0 without a panic, and a
   60s process deadline bounds every child run. Silence or bare process exit
   is not accepted: the close boundary must be observed in stdout.

`agent probe run <scenario> --replay <fixture> --json` then runs the same
capture through the public probe chain with a committed scenario whose
expectations are exactly: the typed refusal payload appears in the observable
transcript (`transcript_contains`), the full 7-frame capture plays
(`frame_count`), and the recorded synthetic terminal reason holds
(`terminal_reason`). The run exits 0 with `"status":"pass"`, proving CLI
composition, replay transport, observation extraction, and typed expectation
evaluation agree end-to-end.

## What is refused versus executed

In v4e nothing reaches a tool executor: the session refuses an unrecognized
name as typed refusal content and completes normally. This lane therefore
stays distinct from the execution-error verticals:

- **v4c** (`s2s-v4c-tool-error-proof.md`) proves a tool call that *fails
  during execution* surfaces a typed `ERROR` event on the delta stream of the
  turn-based ask surface.
- **v4d** owns tool-execution *timeouts*.
- A hallucinated tool name must neither panic, hang, nor surface only as
  untyped text — v4e pins that contract on the realtime session surface.

## Negative control

`TestRegisteredToolControlRejectsUnknownRefusalExpectation` proves the
assertion discriminates instead of labeling every tool call as unknown:

- The committed control pair
  (`s2s-v4e-tool-registered.session.json` /
  `s2s-v4e-tool-registered.scenario.json`) preserves the positive interaction
  shape but requests `read_file`, a name present in the active registry.
- Both scenario documents declare identical expectation blocks, and the test
  asserts the two blocks stay identical, so the control cannot weaken or
  swap the shared refusal expectation.
- The real probe run must fail deliberately: exit code non-zero, summary
  `"failed":1` with `"status":"fail"`, and a result line with `pass:false`
  where only `transcript_contains` fails while `frame_count` and
  `terminal_reason` stay green. Those green outcomes plus an empty result
  `error` field prove the run replayed the full healthy boundary — the loss
  is caused by the registered-name condition alone, not by replay divergence,
  missing fixtures, network access, panic, or the deadguard.
- The failing outcome carries machine-readable expected-versus-actual
  evidence: `expected` is the quoted typed refusal payload and `actual` is
  the quoted transcript, which names `read_file` and contains no
  `unknown_tool` classification.

## Re-running offline

All commands run from `agent-cli/` without credentials or network access
(replay transport only; invalid proxy env in the test makes accidental dials
fail fast):

```console
$ go build -o /tmp/agent ./cmd/agent

$ /tmp/agent --config-dir /tmp/agent-cfg tool --list
Warning: deny patterns are disabled. All commands will be allowed.
append_file
...
read_file             # active registry contains read_file ...
write_file            # ... and never s2s_v4e_unregistered_tool

$ /tmp/agent --config-dir /tmp/agent-cfg session --replay \
    test/integration/testdata/s2s-v4e-tool-unknown.session.json
{"type":"refusal","classification":"unknown_tool","tool_name":"s2s_v4e_unregistered_tool"}
[session closed: fixture_complete]
[session terminal: ...]
# exit 0

$ /tmp/agent probe run \
    test/integration/testdata/s2s-v4e-tool-unknown.scenario.json \
    --replay test/integration/testdata/s2s-v4e-tool-unknown.session.json --json
# exit 0; result line "pass":true then summary "status":"pass"

$ /tmp/agent probe run \
    test/integration/testdata/s2s-v4e-tool-registered.scenario.json \
    --replay test/integration/testdata/s2s-v4e-tool-registered.session.json --json
# exit 1; result line "pass":false with transcript_contains
# expected-vs-actual, summary "status":"fail"
```

Or via the committed tests:

```console
$ go test ./test/integration -run 'TestUnknownToolRefusal|TestRegisteredToolControl' -v
```

No new audio fixtures were required: this vertical is text/refusal scoped and
the existing audio corpus under `go-agent-loop/testdata/audio/` is untouched.

## Relationship to other lanes

- **s2s-v1** proves the baseline text-in/audio-out happy-path session; no tool
  semantics are involved.
- **s2s-v2a** proves basic audio-input transport; v4e carries no audio at all.
- **s2s-v6a** proves the provider *error* taxonomy (auth, disconnect,
  rate-limit, malformed frames) where the provider authors `error` events;
  the v4e refusal arrives inside a *successfully completed* response, and the
  negative control varies only the tool-name condition.
- **v4c/v4d** cover local tool *execution* failures and timeouts after a call
  is dispatched; v4e covers the pre-execution refusal of an unrecognized name
  with a healthy terminal boundary.
