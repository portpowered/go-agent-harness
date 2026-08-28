# Live speech barge-in proof contract

This contract is provider-neutral. A session transport adapter maps its wire
events into the normalized `probe.BargeInEvent` vocabulary, and the oracle
validates the resulting identity-aware sequence. Timestamps and aggregate
counts are supporting evidence only; they do not establish ownership.

## Normalized ledger

Every admitted customer utterance has a stable `InputID` and `TurnID`. The
ledger requires one logical append group with non-empty bytes, one commit, and
one user-turn representation. Physical append frames may share the same
append-group identity.

Every response has a stable `ResponseID`, an owning input/turn identity, and
one terminal disposition:

- `completed` when normal completion wins the race;
- `cancelled` when a live response is superseded by an interrupt; or
- `failed` with an observable reason.

An interrupt records both the response it cancels and the distinct non-empty
input that won the boundary. A response may receive one cancellation only, and
no output event may follow that cancellation or the response terminal event.

Tool calls are independently keyed by `ToolCallID` and their owning response
and turn. Each call must receive exactly one result disposition: `delivered`,
or `rejected`, `cancelled`, or `failed` with a reason. Cancelling the owning
response does not silently dispose of the tool call.

Continuation events identify the replacement response and its input. A clean
session terminal observation is valid only after every input, response, tool
call, and required continuation has reached its documented boundary. Missing,
duplicate, orphaned, wrong-ID, stale, or pending work is a failed proof even if
the process exits with status zero.

Turn-start proofs may additionally forbid output for a response whose first
output is deliberately held behind the speech boundary. This distinguishes an
input-winning race from a response that leaked its first audio or text before
the cancellation boundary.

## Transport adapter mapping

The OpenAI Realtime adapter uses the following illustrative mapping; another
provider can use different wire names while producing the same ledger events.

| Provider boundary | Normalized event | Identity carried by the adapter |
| --- | --- | --- |
| `input_audio_buffer.append` | `input.append` | input/turn and append-group IDs, non-empty byte fact |
| `input_audio_buffer.commit` | `input.commit` | input/turn IDs |
| user item creation | `user.turn` | input/turn IDs |
| `response.created` | `response.created` | response/input/turn IDs |
| output audio or text delta | `response.output` | response ID and non-empty fact |
| `response.cancel` | `response.cancel` | response ID and interrupting input ID |
| `response.done` | `response.terminal` | response ID and normalized disposition |
| tool invocation | `tool.call` | tool-call/response/turn IDs |
| tool result delivery or explicit rejection | `tool.result` | tool-call/response/turn IDs and disposition |
| replacement response | `continuation` | replacement response/input/turn IDs |
| command/session terminal | `session.terminal` | disposition and explicit clean bit |

The adapter must not put raw payloads, customer audio, transcripts, credentials,
or authorization values into the normalized event or its diagnostics.

## Bounded coordination

`BargeInCoordinator` creates one explicit context for event gates, command
awaits, stream drains, fixture barriers, and context-aware observer workers.
`WaitFor` names the expected boundary and uses a positive timeout. A timeout
returns `BargeInWaitError` containing the safe observed sequence and sorted
unresolved identities. `StopAndWait` cancels the shared context and joins
workers within a bounded teardown grace period; workers must honor the context.
