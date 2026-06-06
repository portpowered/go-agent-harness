# Ordering

Messages and deltas have ordering to help with guarantees about the order of messages on retry and on interrupt.

# Message ordering

Each message or delta carries the following ordering fields (see `Message` and `StreamMessage` in `pkg/messages`):

- **GlobalIndex** (INDEX) — The global index of the message/delta in the agent loop. Assigned by the engine when it consumes the item; strictly increasing.
- **ActorProvidedID** — Optional unique identifier from the actor that sent this message (for duplicate detection and replace semantics). Not yet used by the engine.
- **ActorProvidedIndex** — Index as understood by the actor (e.g. for reordering on packet loss). Can be per-actor or per-stream when an actor has multiple logical streams (e.g. parallel tool calls). For model deltas, the engine also uses this to record position in the conversation delta buffer.
- **ActorID** — Participant that produced this message (Model, User, Tool, Kernel).

*Planned (not in current structs):* **ActorStreamID** — When an actor has multiple concurrent streams (e.g. parallel tool responses), a unique identifier for the stream. Currently streams are distinguished by order and content only.

# Implementation status

| Behavior | Status |
|----------|--------|
| Global index assignment on consumption | **Implemented** — `GlobalOrdering.assignStreamOrdering` / `assignMessageOrdering` in `pkg/engine/ordering.go`. |
| Deltas and full messages appended to history | **Implemented** — `UpdateWorldHistory` moves inputs into `ConversationBuffer` and `ConversationDeltaBuffer`. |
| Delta assembly (message.start → buffer, message.end → emit) | **implemented** — Deltas are appended; full messages come from a separate path (e.g. model outbox). No single component “pieces together” deltas into messages or validates completeness. |
| Duplicate detection (ActorProvidedID + ActorID) | **Not implemented** — Every consumed item is appended; no discard on match. |
| Retry backwards (same → discard, replace → terminate effects & restart from index) | **Not implemented** — No checks or restart-from-index logic. |
| Out-of-order acceptance and reordering by ActorProvidedIndex | **Not implemented** — Consumption order is strict (channel order); global index is assigned sequentially. |
| Customer interrupt at a specific index (rewind and run from state at index N) | **Not implemented** — No rewind or “run from state at index N.” |
| Customer interrupt with stop (pause, clear buffers) | **Partially implemented** — `TerminateLoop` and buffer clearing exist; exact semantics may vary. |

# Internal implementation details

## Engine

Global message ordering is maintained by **GlobalOrdering** in `pkg/engine/ordering.go` (not a separate “messageBufferReader”). It consumes one item per tick from participant outboxes (deltas preferred, then full messages), assigns a strictly increasing **GlobalIndex** to each, and appends to `LoopState.Inputs`. After execution, **UpdateWorldHistory** moves those inputs into **History** (ConversationBuffer and ConversationDeltaBuffer). **FlushInputs** clears the input slices for the next tick.

## Actors

Actors send messages/deltas to outboxes. The engine assigns GlobalIndex at consumption time. Actors may set ActorProvidedIndex for their own ordering or reordering; the engine does not currently use it for duplicate or out-of-order handling.

## Message construction (intended behavior)

*Target behavior (not fully implemented):* As deltas arrive, a component would assemble them into logical messages: on message.start, start a new buffer; on message.end, emit the message and clear the buffer. Non-ordered components would be discarded. If any required part is missing, the entire incomplete message would be discarded. Currently, deltas are appended to ConversationDeltaBuffer and full messages are added via separate paths; no assembly or completeness check is performed.

# Scenario examples

## Customer interrupts with stop

Customer sends a message to stop the system right now. The system stops immediately, actors are paused, and current input buffers are cleared. *Partially implemented* (TerminateLoop and flush exist; full pause/clear semantics may vary).

## Parallel tool call streams

Tool calls can be run in parallel; each tool responds in its own logical stream. Example from the actor’s perspective (STREAM_ID is planned; not yet in structs):

```
[MESSAGE.START] [ACTOR_INDEX 0] [ACTOR TOOL] [STREAM_ID 1]
[TOOLCALL.START] [ACTOR_INDEX 0] [ACTOR TOOL] [STREAM_ID 2]
[TOOLCALL.START] [ACTOR_INDEX 0] [ACTOR TOOL] [STREAM_ID 3]
[TOOLCALL.DELTA] [ACTOR_INDEX 1] [ACTOR TOOL] [STREAM_ID 2]
[TOOLCALL.DELTA] [ACTOR_INDEX 1] [ACTOR TOOL] [STREAM_ID 3]
[TOOLCALL.END] [ACTOR_INDEX 2] [ACTOR TOOL] [STREAM_ID 2]
[TOOLCALL.END] [ACTOR_INDEX 2] [ACTOR TOOL] [STREAM_ID 3]
[MESSAGE.END] [ACTOR_INDEX 1] [ACTOR TOOL] [STREAM_ID 1]
```

Tool call deltas within the stream can be ordered and interleaved. *ACTOR_PROVIDED_INDEX* can be per-stream so that parallel streams each have their own sequence.

## Customer interrupts with message at time (intended)

Assume the system is at global index 10. The customer wants to interrupt at index 9. The customer would send something like [MESSAGE.START] with a requested index 9 and ACTOR USER. The system would interrupt execution, rewind world history to that index, and reinitiate execution from that state. *Not implemented* — the engine assigns GlobalIndex on consumption and does not support user-specified rewind index or rewind/restart.

## Message duplicate from an inferencer (intended)

If a message is sent in duplicate, the system would check ACTOR_PROVIDED_ID and ACTOR_ID against existing messages and discard duplicates. *Not implemented* — all consumed items are currently appended.

## Retrying inferencer — backwards in history (intended)

When the model fails partially and retries, it may resend from an earlier delta. The system would: if the same message (by ACTOR_PROVIDED_ID/ACTOR_ID), discard; if it is meant to replace a previous message, terminate effects (e.g. tool calls) based on the current message and restart processing from that new message. *Not implemented* — no duplicate/replace check or restart-from-index.

## Out-of-order messages (intended)

If a delta arrives out of order, the system would accept it and place it in the buffer according to ActorProvidedIndex (or similar), preserving global ordering. *Not implemented* — consumption is strictly in channel order; no reorder buffer.

## Tests

- Test (correct global order)
- Test (incorrect global order)
- TestCustomerInterruptWithMessageAtTime
- TestCustomerInterruptWithStop
- TestMessageDuplicateFromInferencer
- TestRetryingInferencerBackwardsInHistory
- TestOutOfOrderMessages
