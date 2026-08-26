# Production session config-aware tool filtering

Status: prerequisite delivered on 2026-08-26. This document records the
contract for the later v5a retry; it does not claim v5a is complete.

## Runtime contract

The wire-composed `agent session` command loads the effective configuration
directory at invocation time, after Cobra resolves `--config-dir` and before a
session provider is opened. One loaded config snapshot is passed to a
config-aware capability factory, which creates the filtered registry, its
registry-backed executor, and the tool definitions advertised to the session
loop.

An empty `tools.list` preserves the all-enabled policy, including `sleep`. A
list entry `{id: sleep, enabled: false}` removes only `sleep`; omitted IDs such
as `read_file` remain enabled. The same executor/definition pair is threaded
through injected-live, record, and replay runtime plans. Live provider
constructors receive the definitions in their initial session configuration;
strict websocket replays retain their captured outbound sequence while the
replay loop still uses the selected filtered executor and definitions.

Invalid selected configuration fails with a session config-loading error before
the session inferencer connects. Composition itself remains inert: no config
file is loaded and no request-scoped filtered registry is built until command
execution.

## Hermetic behavioral evidence

`agent-cli/test/integration/s2s_prereq_session_config_tool_filter_test.go`
drives the real CLI router with normal argv and a temporary `--config-dir`. It
replaces only the external session inferencer with a deterministic in-memory
port and retains the production registry-backed executor. The matrix observes
the runtime contract through advertised definitions, call IDs, correlated
results, and terminal behavior:

- empty list: `sleep` is advertised and
  `prereq-default-sleep` with `{"duration":"0s"}` returns exactly
  `Slept for 0s (no-op).`;
- disabled sleep: `sleep` is absent and its request returns a failed tool
  result, while omitted `read_file` stays advertised and reads isolated
  temporary test data;
- malformed config: the session port is never connected.

Provider-specific `function_call_output` translation, serialization, and
result forwarding remain owned by PR #181. v5a must be rerun only after this
prerequisite and PR #181 are both available; this prerequisite is not a v5a
completion claim.
