# WebMCP probe examples

These examples are executable `probe.scenario.v2` documents for the public
`agent probe run` command:

```sh
go run ./agent-cli/cmd/agent probe run \
  agent-cli/testdata/probe-scenarios/webmcp/happy-page-tool.scenario.json \
  agent-cli/testdata/probe-scenarios/webmcp/stale-ref-recovery.scenario.json \
  --json \
  --recording-root /tmp/webmcp-probe-recordings
```

The default browser executor is hermetic. The browser-script fixtures and
provider captures are replayed offline, fake time and IDs are deterministic,
and no browser or provider network is contacted. The happy-path scenario
discovers `read_state`, invokes it with structured input, and verifies the
structured result. The recovery scenario keeps the first reference across a
navigation, records its typed `stale_tool_ref` rejection, refreshes the
catalog, and retries with the generation-two reference.

Both scenarios should print one passing scenario JSON line and a passing
summary. With `--recording-root`, each result also points to one finalized
manifest-v2 bundle containing the provider capture, redacted semantic browser
events, independent page-state snapshot, workspace snapshot, and objective
evidence. The recovery browser event stream makes the generation change,
stale rejection, rediscovery, and successful retry observable in order.

Real browser execution is an explicit opt-in:

```sh
go run ./agent-cli/cmd/agent probe run <scenario.json> \
  --browser-executor real
```

Real mode requires the configured WebMCP browser endpoint and does not fall
back to the hermetic fixture. These committed examples are intended for the
offline default; use a scenario whose browser steps target the configured
browser when validating a live endpoint.
