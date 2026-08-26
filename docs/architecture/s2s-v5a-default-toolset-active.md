# s2s v5a — default toolset active lease disposition

Status: **lease-exhausted; the CLI proof is not runnable on `origin/main`**
(2026-08-26).

This lane is scoped to `agent-cli/test/integration/**`,
`docs/architecture/**`, and additive audio fixtures. The requested proof cannot
be implemented honestly inside that lease because the shipped realtime session
composition and provider-wire result contract are not present on the base
commit. No new audio fixture is justified: the requested scenario is seeded by
text.

## Missing shipped contract

The intended customer-facing invocation is the production-composed root
command, for example:

```text
agent session --replay <v5a-default-sleep.session.json> --prompt "invoke the default sleep tool"
```

The command deliberately has no `--tools` argument. On `origin/main`:

- `agent-cli/internal/cli/session.go` has no session tool flag and constructs
  `SessionRunOptions` without a tool executor or tool definitions.
- The generated wire graph creates the session command as
  `cli.NewSessionCommand(askFlags, globalFlags, sessionInferencer)`. The
  registry-backed executor is supplied to `agent.NewExecutor` for the
  stateless ask/chat path, but it does not cross the session command boundary.
- `agent-cli/internal/services/session_live.go` constructs the duplex loop with
  only `WithMode(engine.DuplexSession)` and `WithSessionInferencer`; it has no
  `WithTools`/`WithToolExecutor` path.
- Although the configuration and internal registry know the default `sleep`
  tool, the session runtime does not resolve that registry or advertise its
  definitions. The existing `tools.list` `enabled: false` setting therefore
  cannot drive the requested disabled-sleep control through the CLI.
- The OpenAI realtime outbound translation currently accepts audio, text, and
  message-end events but has no `StreamTypeToolCallEnd` mapping to a correlated
  `conversation.item.create` / `function_call_output` provider event. Session
  executor composition alone would not satisfy the outbound-result assertion.

Adding those composition, runtime, and provider-wire contracts requires
production changes outside this lane's changed-path lease. In particular, the
positive test must not replace the production root with a direct service,
agent-loop, registry, executor, or replay-helper call just to make a proof pass.

## Required follow-up proof

After the upstream session-tool and provider-result contracts land, add the
integration proof under `agent-cli/test/integration/**` and rerun it with:

```text
cd agent-cli
go test ./test/integration -run 'TestSessionCommand_DefaultToolSetActive|TestSessionCommand_DefaultToolSetActive_DisabledSleepNegativeControl' -count=1 -v
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

The negative-control case must use the same production CLI, capture, text seed,
and no-`--tools` argv while loading a config that explicitly disables `sleep`:

```yaml
tools:
  list:
    - id: sleep
      enabled: false
```

It must apply the same exact correlated-success-result assertion and pass only
when replay rejects the run with expected-versus-actual evidence (for example,
the missing `function_call_output` for `v5a-default-sleep`). A tool-call
receipt, a silent no-op, or a non-zero result without the replay mismatch
evidence is insufficient.

This disposition records no v5a CLI success claim and no coverage claim for
v1, v2a, v6a, live providers, or internal-only tests. Existing replay smoke
tests remain the baseline checks until the missing production contracts are
filed and landed.
