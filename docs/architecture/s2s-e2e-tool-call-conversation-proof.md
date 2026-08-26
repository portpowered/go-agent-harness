# s2s depth-5 milestone — tool-call conversation reflects the real tool result

Status: **achieved and formally proven in-repo** (2026-08-26).

The depth-5 milestone of the s2s program — a customer asks by voice, the agent
calls a specific CLI-registered tool, and the agent's spoken reply states what
the tool ACTUALLY returned — is proven by a hermetic, deterministic,
CI-gating test with no network and no credentials.

## What is proven

`TestSessionToolCallConversationSpokenReplyReflectsRealToolResult`
(`agent-cli/test/integration/session_tool_result_conversation_test.go`) drives
the real `agent session --replay <capture> --audio-in <wav> --audio-out
<path>` command surface over the record/replay WebSocket transport and closes
the full causal chain:

1. **One named tool call.** The injected `messages.ToolExecutor` (composed
   through the production wire graph, `wire.InitializeMockAgentCLI`) observes
   exactly one invocation of `get_weather` with arguments `{"city":"Lisbon"}`.
2. **The real result reaches the provider.** The replayed exchange contains
   exactly one client-to-server `conversation.item.create` whose item is
   `function_call_output`, and its `output` equals the executor's runtime
   return value verbatim. Replay outbound validation matches every frame
   payload-for-payload, so a clean completion proves the live session sent it.
3. **The spoken reply quotes the result.** The fixture's server-side transcript
   deltas are authored from the same runtime return value ("24 degrees",
   "clear skies") and are positioned strictly AFTER the function_call_output
   record: the replayer withholds them until the executed result is delivered.
   The rendered transcript on the session output must quote those values —
   they appear nowhere else in the exchange.
4. **Audible speech.** The recorded `--audio-out` WAV passes RMS > 500 energy
   and duration bounds derived from the scripted reply window.

## Non-vacuity control

`TestSessionToolCallConversationDifferentResultFailsReflection` runs the
identical fixture but the executor returns different content at runtime. The
outbound `function_call_output` diverges from the fixture at that exact frame;
replay terminates deterministically (never via timeout) before the authored
reply is served, and the shared transcript-reflection assertion fails naming
the missing expectation values.

## Production seams this proof rests on

- `agentloop.WithToolExecutor` wiring in the composed live session path
  (`agent-cli/internal/services/session_live.go`), so provider tool calls
  execute through the composed CLI executor instead of the loop default.
- Tool-result delivery onto the provider wire (`subsystems.ToolResultForwarder`
  plus the OpenAI Realtime outbound `function_call_output` translation), which
  gives the replay gate something real to validate against.
- Session-lifecycle continuation: an awaiting audio session no longer ends at
  an assistant MESSAGE.END whose turn left unresolved tool calls; the runner
  holds until the follow-up response carries the spoken reply
  (`runAgentLoopSessionStream`). The ToolRunner's role=tool MESSAGE.END is
  treated as an internal boundary, never a terminator.
- Transcript rendering: assistant speech transcripts are printed on the
  session output alongside text deltas, so voice-session logs show what was
  actually said (`writeSessionReplayMessage`).

## Historical context

The v4a lane (#163) proved call -> executor -> result ordering over this
transport but its scripted transcript was static text, so nothing bound the
spoken reply to the executor's actual return value. This lane closes that gap
end to end through the public CLI surface.

## How to run

```sh
cd agent-cli
go test ./test/integration/ -run 'TestSessionToolCallConversation' -v -count=1
```
