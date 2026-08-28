# Room lifecycle diagnosis

The room lifecycle diagnosis is driven by
`TestRunRoomLifecycleDiagnosis_ForcedOrderings` in
`agent-cli/internal/services/session_room_lifecycle_test.go`. The test uses
per-participant connection gates and explicit transport controls, so the
observed sequence does not depend on goroutine or callback arrival order. Each
observation is keyed by participant ID and records connection attempts and
outcomes, created-session ownership, close count, terminal callback identity
and reason, and unresolved owned work at return.

The product contract used for classification is:

- every manifest participant receives exactly one initial connection outcome;
- a created session is closed exactly once by its owner before room return;
- every participant receives exactly one terminal disposition identified by
  its own manifest ID; and
- a clean return has no unresolved owned work.

## Terminal-cause correction

The room service now latches the first observed participant terminal cause.
Transport completion is classified as disconnected, typed provider close is
classified as disconnected, typed client/session close is classified as ended,
and a non-cancellation participant error is classified as error. The
coordinator marks intentional stopping before cancelling participant contexts,
so sibling cancellation cannot replace the causal participant's result or
turn a later teardown into a provider disconnect. The room context is isolated
from caller cancellation until that coordinator transition is recorded.

`TestRunRoomLifecycleTerminalCausePrecedence` covers typed provider close,
typed session close followed by transport completion, and transport completion
followed by a later typed close. Each subtest waits for the target callback by
participant ID, validates the full identity-aware ledger, and verifies owned
session closure exactly once.

## Forced-ordering matrix

| Controlled ordering | Identity-correlated observation | Classification |
| --- | --- | --- |
| Target connection fails before sibling connection completion | Before the correction, the target publishes its forced dial failure and the room context cancels the gated sibling and observer attempts before they can complete. The corrected barrier drains the two sibling outcomes first, then rolls back the created sessions. | Runtime lifecycle bug: startup cancellation preempted the admission barrier and could suppress viable sibling outcomes. The ledger/oracle is not the cause. |
| Sibling connection succeeds before target failure | The sibling has a successful outcome and a created session before the target publishes its forced failure; the corrected barrier still admits the gated observer and records all three outcomes before rollback. | Runtime lifecycle bug for the same incomplete admission barrier. The successful siblings remain identity-correlated and are not mistaken for the target. |
| Target transport ends before sibling terminal completion | The target callback is identified as `target/disconnected`; siblings remain admitted and do not receive the target callback. | The runtime behavior is identity-safe in this controlled case. Any test that consumes the first callback as though it must be the target is a fixture/oracle bug. |
| Sibling activity is released at the same controlled boundary as target transport end | The target terminal record remains keyed to `target`; sibling activity does not create a sibling terminal result or inherit the target reason. | The runtime behavior is identity-safe in this controlled case; a first-callback-only interpretation is a fixture/oracle bug. |
| Target transport end precedes explicit room cancellation | The target's disconnected callback and one owned-session close are observed before cancellation; sibling terminal callbacks follow only during explicit room teardown. | The runtime preserves the causal target observation in this forced sequence. Replacing it with the later coordinator cancellation would be a runtime lifecycle bug. |

The two reported symptoms therefore separate cleanly: the startup symptom is
a runtime lifecycle bug, while the disconnect symptom is a fixture/oracle bug
when the fixture consumes whichever callback arrives first. No scenario in
this diagnosis produces a wrong participant ID or reason, so the evidence does
not claim a combined runtime-and-oracle failure.

`TestRunRoomLifecycleDiagnosis_NegativeControls` proves that the oracle fails
closed for a first-callback-only consumer, a missing connection outcome, an
unclosed or multiply closed session, a duplicate callback, a wrong identity or
reason, and unresolved work at return. Its waits use positive bounded timers
with participant and phase names; no sleep or retry loop is used for
correctness synchronization.
