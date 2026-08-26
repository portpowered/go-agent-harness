# s2s v5a — default toolset active lease disposition

Status: **lease-exhausted; the executable CLI proof is blocked by production
prerequisites on `origin/main`** (2026-08-26).

This lane is scoped to `agent-cli/test/integration/**`,
`docs/architecture/**`, and additive audio fixtures. The current base now
passes the composed registry executor into the duplex session loop, but the
provider-visible tool-result path is still absent. The negative control also
needs a config-aware session executor. Both missing contracts require
production changes outside this lease. No new audio fixture is justified: the
requested scenario is seeded by text.

## Missing shipped contracts

The intended customer-facing invocation is the production-composed root
command, for example:

```text
agent session --replay <v5a-default-sleep.session.json> --prompt "invoke the default sleep tool"
```

The command deliberately has no `--tools` argument. The first part of the
production composition is now present on `origin/main`:
`agent-cli/internal/wire/composition.go` creates the default registry executor,
`agent-cli/internal/cli/session.go` passes it through `SessionRunOptions`, and
`agent-cli/internal/services/session_live.go` installs it in the duplex loop.
The remaining gaps are:

- `go-agent-loop/pkg/agentloop/agent_loop.go` has no session-only forwarder
  from assembled tool results to `ModelRunner.UserEventInbox`.
- `go-llm-gateway/pkg/providers/openai/session_events.go` has no
  `StreamTypeToolCallEnd` translation to a correlated
  `conversation.item.create` / `function_call_output` provider event.
  Executor composition alone therefore cannot satisfy the outbound-result
  assertion.
- `agent-cli/internal/wire/composition.go` constructs the production registry
  with `tools.NewToolRegistry()` rather than the loaded config. The
  `tools.list` `enabled: false` setting consequently cannot drive the required
  disabled-sleep control through the same CLI until a config-aware session
  executor boundary is added.

These are production changes outside this lane's changed-path lease. The
integration proof must not replace the production root with a direct service,
agent-loop, registry, executor, or replay-helper call just to make it pass.

## Executable blocker evidence

`agent-cli/test/integration/s2s_v5a_default_toolset_active_test.go` invokes the
production CLI root with `session --replay ... --prompt ...`, without a
`--tools` argument, and builds the synthetic T1 capture at runtime. The capture
requests `sleep` with call ID `v5a-default-sleep` and arguments
`{"duration":"0s"}`, then requires the exact correlated result
`Slept for 0s (no-op).` followed by a normal continuation and session close.

On the current base, the test reaches the expected outbound result slot but
the replay rejects it: the actual event is another `conversation.item.create`
containing the internal text-seed marker `\x00agent-cli-session-text-seed:1`,
not the required `function_call_output`. The test is intentionally an
executable prerequisite canary; it must remain a real production-root proof,
not be weakened with an injected executor or a silent skip.

## Required follow-up proof

After the provider-result contract lands, rerun the positive proof with:

```text
cd agent-cli
go test ./test/integration -run '^TestSessionCommand_DefaultToolSetActive$' -count=1 -v
```

The positive case must:

1. build the CLI through the production wire root and invoke `session` with
   the replay capture and recorded text seed, without `--tools`;
2. replay a provider request for `sleep` with call ID `v5a-default-sleep` and
   arguments exactly `{"duration":"0s"}`;
3. validate the exact correlated outbound provider result
   `Slept for 0s (no-op).`; and
4. validate a subsequent provider/session completion event so result
   reinjection and normal continuation are demonstrated, rather than merely
   observing a definition, tool call, CLI text, or exit zero.

The negative-control case still requires the config-aware session executor
prerequisite. Once it lands, it must use the same production CLI, capture, text
seed, and no-`--tools` argv while loading a config that explicitly disables
`sleep`:

```yaml
tools:
  list:
    - id: sleep
      enabled: false
```

It must apply the same exact correlated-success-result assertion and pass only
when replay rejects the run with expected-versus-actual evidence (for example,
the missing `function_call_output` for `v5a-default-sleep`). A tool-call
receipt, a silent no-op, or a non-zero result without evidence that the exact
success payload was not returned is insufficient.

This disposition records no v5a CLI success claim and no coverage claim for
v1, v2a, v6a, live providers, or internal-only tests. The provider-result and
config-aware-session prerequisites should be filed as separate production work
items; once they land, the focused positive canary above and the corresponding
negative-control test can be run as the review evidence.
