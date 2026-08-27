# s2s v5a — default toolset active

Proof status: **implemented and locally verified** (2026-08-27). The proof is
hermetic and uses the production-composed `agent session` CLI; it does not
claim v1, v2a, or v6a coverage.

## What is proven

`agent-cli/test/integration/s2s_v5a_default_toolset_active_test.go` invokes
`wire.InitializeAgentCLI` and executes the real `session --replay` command.
The scenario uses a text seed as a positional session argument and deliberately
passes no `--tools` flag. It requires no credentials, network, or audio fixture.

The synthetic T1 replay begins with the committed OpenAI Realtime handshake and
then requests the default `sleep` tool with:

- call ID: `v5a-default-sleep`;
- arguments: `{"duration":"0s"}`; and
- expected result: `Slept for 0s (no-op).`.

The replay transport validates the exact outbound
`conversation.item.create` containing a `function_call_output` correlated to
`v5a-default-sleep`. It withholds the follow-up response until that payload is
accepted. The positive test then requires the continuation text
`Sleep tool result reinjected.` and the normal session close reason
`v5a-default-toolset-complete`. Tool advertisement, receipt of a tool call,
CLI output alone, or exit zero cannot satisfy this proof.

The stream-only session path also emits its ordinary follow-up text trigger
and `response.create` after the flat tool result. Those events are captured
explicitly so the test validates the complete provider-facing sequence rather
than accepting an uncorrelated result.

## Disabled-sleep negative control

`TestSessionCommand_DefaultToolSetActive_DisabledSleepRejectsSuccess` invokes
the same production CLI, replay capture, text seed, and argument shape with no
`--tools` flag. Its temporary `config.yaml` explicitly contains:

```yaml
tools:
  list:
    - id: sleep
      enabled: false
```

The replay still requests `v5a-default-sleep` with `{"duration":"0s"}` and
still expects the exact successful result payload. Because the config-aware
session registry excludes `sleep`, the CLI cannot produce that payload. The
test passes only when the CLI returns `gateway.ErrReplayMismatch`, exposes a
typed mismatch whose expected slot is outbound sequence 9 and whose actual
event is `conversation.item.create`, and does not reach the successful
continuation. A received tool call, a silent no-op, or a generic non-zero exit
does not satisfy the control.

## Production seams exercised

The proof depends on the production contracts already present on the base:

- the wire-composed session command loads the selected config directory and
  builds the default registry executor and tool definitions from that snapshot;
- the duplex loop forwards completed flat tool results to the session model
  runner; and
- the OpenAI Realtime provider translates `StreamTypeToolCallEnd` into the
  correlated `function_call_output` provider event.

The positive capture and the negative control both fail if any of those seams
are disconnected. No internal service, agent loop, registry, executor, or
replay helper is used as the behavior under test.

## How to run

Positive proof:

```sh
cd agent-cli
go test ./test/integration -run '^TestSessionCommand_DefaultToolSetActive$' -count=1 -v
```

Disabled-sleep control:

```sh
cd agent-cli
go test ./test/integration -run '^TestSessionCommand_DefaultToolSetActive_DisabledSleepRejectsSuccess$' -count=1 -v
```

The combined focused run is:

```sh
cd agent-cli
go test ./test/integration -run '^TestSessionCommand_DefaultToolSetActive' -count=1 -v
```
