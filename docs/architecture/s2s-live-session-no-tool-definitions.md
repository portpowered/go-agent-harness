# Live session tool advertisement and round trip

The live probe failure was a provider-discoverability failure: the session
runtime had an executor, but its initial OpenAI Realtime `session.update` did
not carry `session.tools`. The model therefore correctly reported that no
`exec` tool was available.

The production session command now resolves its capabilities from the config
selected by `--config-dir`. One config-scoped registry supplies both the
`RegistryExecutor` and the `ToolDefinition` slice. For an OpenAI Realtime
session, the provider's first update contains the registry-derived GA function
shape:

```json
{
  "type": "function",
  "name": "exec",
  "description": "Execute a shell command and return its output. Use with caution.",
  "parameters": {
    "type": "object",
    "properties": {
      "command": {"type": "string", "description": "The shell command to execute"},
      "working_dir": {"type": "string", "description": "Optional working directory for the command"}
    },
    "required": ["command"]
  }
}
```

The focused credential-free production-root proof is:

```text
go test ./test/integration -run 'TestSessionCommand_(DefaultRegistryExecRoundTripInStrictOpenAIReplay|StrictOpenAIReplayRejectsMissingExecAdvertisement)' -count=1 -v
```

It runs `wire.InitializeAgentCLI`, supplies only a temporary config and a
strict OpenAI websocket capture, and requires the first outbound update to
contain the non-empty `exec` schema. The positive replay then emits exactly
one provider `function_call` with the stable call ID `call_exec_probe_1`. Its
arguments ask the real default registry executor to append the marker to a
temporary invocation log and echo it. The replay requires one exact
`conversation.item.create` / `function_call_output` carrying the same call ID
and `PROBE_TOOL_MARKER_9182\n`, followed by a second response and
`session.closed`; the temporary log independently proves exactly one real
execution. A second function result, a different call ID, or different output
diverges before the continuation can complete. Historical OpenAI
captures without a `tools` field remain replayable; a capture that includes
`tools` opts into the selected definitions and fails on omission, null, or
schema divergence.

The provider-wire result path is session-only: the agent-loop
`ToolResultForwarder` delivers each assembled result once to the session
runner, and the OpenAI adapter serializes it as
`conversation.item.create` / `function_call_output` without changing the
plain-text input event shape. The strict prompt-seeded replay includes the
production loop's follow-up user-text/response request before the queued tool
result; the provider continuation is still gated behind the exact correlated
result. The shared result-forwarding and OpenAI translation changes from PR
#181 are reconciled here narrowly to complete this proof.

This session-only path does not alter the stateless agent ask/chat routes.
Config exclusions continue to remove a tool from both the advertised
definitions and the registry executor, while direct no-tools session callers
retain the no-tools path.
