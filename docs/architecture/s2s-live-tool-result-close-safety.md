# S2S live tool-result close safety

This document defines the lifecycle contract for provider-requested tool
results in a live session. It covers the session runner's ordinary live path;
the duration controller has a separate admission/teardown owner and remains a
separate lease boundary.

## Outstanding state

When the session plan composes a tool executor, the session progress observer
owns a synchronized set keyed by the provider's non-empty `call_id`. A
provider tool event in explicit no-tools mode remains covered by the existing
unexecutable-tool diagnostic and does not create a delivery obligation because
there is no result-producing executor.

1. A completed provider function-call event creates one outstanding obligation.
   A repeated event for the same ID is idempotent, and an empty ID is not
   admitted because it cannot be correlated.
2. Tool execution completion, local result assembly, and enqueueing into the
   loop's user-event inbox do not resolve the obligation.
3. The obligation resolves only after the correlated `TOOLCALL.END` result
   crosses `messages.SendSessionWithOutcome` and returns
   `SessionSendSucceeded`. This is the provider-facing acceptance boundary.
4. Cancelled, timed-out, closed, buffer-full, and terminal-failure sends leave
   the ID outstanding. When exposed, the first non-success send status is
   retained for the terminal error. An acceptance or rejection observation may
   arrive before the outer observer consumes the provider call event; the
   state machine reconciles either order by ID.

The set is not a scalar counter. Multiple IDs remain independently unresolved,
and accepting one ID cannot clear another. IDs in errors and diagnostics are
deduplicated and sorted with Go lexical ordering.

## Completion and timeout

For scheduled live input, automatic `SESSION.CLOSE` requires both existing
completion conditions — every scheduled input was accepted and its assistant
turn crossed `MESSAGE.END` — and an empty outstanding set. A result accepted
after the final `response.done` wakes the session runner and re-evaluates this
predicate, so no additional provider response is needed. Exactly one close is
sent after the predicate becomes true.

Each invocation still uses the existing per-call `ToolExecutionTimeout`. A
successful, failed, or timed-out executor result follows the same provider
acceptance rule; a timeout is not delivery evidence until its correlated result
is accepted. If a terminal path occurs first, teardown cancels the remaining
work and reports the IDs rather than treating that cancellation as success.

## Premature termination contract

`ErrSessionUnresolvedToolResults` is the stable sentinel. A
`SessionUnresolvedToolResultsError` carries `CallIDs` and any observed
non-success `SendStatuses`. Its human-readable form always identifies that
tool results were not delivered and lists the unresolved IDs. The original
terminal cause is retained when it is joined with this typed error.

The same typed failure is returned when a provider close, caller/loop close,
context cancellation, result-send rejection, or another terminal path leaves
IDs outstanding. A clean run with an empty set keeps its existing nil-error
close behavior.

The canonical structured diagnostic remains exactly one `session_failure`
record per terminal failure. For an unresolved-result failure it adds:

| Field | Contract |
| --- | --- |
| `unresolved_tool_result_count` | Decimal number of unresolved IDs. |
| `unresolved_tool_call_ids` | Comma-and-space separated IDs in deterministic lexical order. |

The fields are present on the existing failure record, not emitted as a second
terminal record. The observer's exactly-once guard prevents repeated close or
drain observations from duplicating the diagnostic. A bare `client_close` with
unresolved IDs therefore cannot produce a clean exit: the service returns the
typed error and the CLI process exits non-zero through its normal command error
boundary.

## Behavioral proof

`agent-cli/test/integration/session_tool_result_failure_test.go` drives the
exported `services.RunSession` composition seam with a credential-free session
double. It proves both a terminal provider close while an executor is blocked
and a rejected `buffer_full` result send. The assertions use typed errors,
provider send outcomes, human CLI-facing error text, and the stable diagnostic
fields rather than source inspection.
