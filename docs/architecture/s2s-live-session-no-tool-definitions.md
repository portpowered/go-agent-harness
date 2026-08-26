# Live session tool advertisement

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
go test ./test/integration -run '^TestSessionCommand_DefaultRegistryAdvertisesExecInStrictOpenAIReplay$' -count=1 -v
```

It runs `wire.InitializeAgentCLI`, supplies only a temporary config and a
strict OpenAI websocket capture, and requires the first outbound update to
contain the non-empty `exec` schema. Historical OpenAI captures without a
`tools` field remain replayable; a capture that includes `tools` opts into the
selected definitions and fails on omission, null, or schema divergence.

The intended end-to-end follow-up uses one stable call ID and the harmless
command `echo PROBE_TOOL_MARKER_9182`, then requires the exact marker result in
the correlated OpenAI `function_call_output` before accepting the terminal
response. That result-forwarding contract is outside this lane's lease: the
agent-loop result forwarder and OpenAI Realtime `function_call_output`
translation are owned by PR #181. This change deliberately records the
advertisement evidence without adding a knowingly red canary for that
out-of-lease boundary.

This session-only path does not alter the stateless agent ask/chat routes.
Config exclusions continue to remove a tool from both the advertised
definitions and the registry executor, while direct no-tools session callers
retain the no-tools path.
