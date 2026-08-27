# Live-shaped barge-in contract and collision matrix

## Purpose and boundary

This document defines the customer-visible correctness contract before the
validation fixtures for `s2s-validate-live-shaped-barge-in-v2` are designed. It
is independent of a particular provider implementation. The event names below
are examples of observable boundaries in the shipped session wire; the
contract is about the ordered behavior and identities, not about an aggregate
exit code.

The claim is narrow: when non-empty customer speech collides with an agent
turn, the session must preserve both sides of the conversation and must expose
what happened to every piece of in-flight work.

## Product correctness contract

For every collision, the session maintains an identity-aware ledger containing
the customer input turn, the active response, any tool call/result, the
interrupt boundary, replacement response, and final session disposition.
Sequence/order is authoritative; wall-clock timestamps and total event counts
are supporting evidence only.

The customer-visible contract is:

1. **Prompt supersession.** If a non-terminal response is superseded by
   non-empty customer speech, provider cancellation is sent at most once for
   that response and its output stops at the cancellation boundary. No audio or
   text from that response may appear after the boundary.
2. **Exactly-once input.** The interrupting utterance has one input identity,
   one commit, and one user-turn representation. It is not silently dropped,
   duplicated, merged into the wrong turn, or treated as an empty input.
3. **Distinct replacement.** A replacement response has a distinct response
   identity and is processed once. Its output is not attributed to the
   superseded response or to another customer turn.
4. **Explicit in-flight disposition.** Every admitted response, turn, tool
   call, and tool result ends in an observable valid state: completed,
   superseded/cancelled, explicitly rejected/cancelled with a reason, or
   failed with an observable error. A pending item is not equivalent to a
   completed item.
5. **Truthful termination.** A clean success is valid only after all required
   ledger items have an observable disposition and the session has emitted its
   terminal observation. A process that exits cleanly while an interruption,
   response, tool call, tool result, or turn is unresolved is a failure.
6. **Continued usability.** After a collision, a later non-empty input can be
   committed and produces the expected distinct response in the same session
   within the test bound.

These rules reject both loud failures (wrong provider identity, duplicate
cancellation, or an invalid wire sequence) and quiet false successes (lost
input, stale output, orphaned tool work, a hang, or a clean-but-wrong exit).

## Event-ledger vocabulary

The deterministic and live validators record only the minimum safe identity
and ordering evidence:

| Ledger entry | Required observation |
| --- | --- |
| Input | A stable input/turn ordinal or provider item identity, non-empty audio/input boundary, one commit, and one user-turn representation |
| Response | `response_id`, creation and terminal events, whether output was emitted before the collision, and the owning input/turn |
| Supersession | The ordered speech boundary, the response it supersedes, exactly-one cancel when required, and zero post-cancel audio/text deltas |
| Tool | `tool_call_id`, owning response/turn, call/result direction, and one explicit disposition: delivered, rejected/cancelled with reason, or another documented product-valid terminal state |
| Continuation | A later input identity and a distinct replacement response after the collision |
| Session | Terminal state/reason, unresolved-item count, and whether clean success was permitted |

Provider event names such as `response.created`,
`response.output_audio.delta`, `response.done`,
`input_audio_buffer.speech_started`, `input_audio_buffer.commit`, and
`response.cancel` are useful boundaries when present. A validator must still
bind each event to the response, turn, or tool identity instead of inferring
causality from a count.

## Collision matrix

The matrix is the test-design contract. “Cancel” means one provider-bound
cancellation for the named non-terminal response. “No cancel” is a positive
outcome only when the response became terminal before the interrupt boundary;
it is not permission to ignore a still-live response.

| Collision shape and trigger boundary | Cancel / no-cancel rule | Identity and state requirements | Allowed terminal state | Deterministic evidence | Live evidence and product-risk rationale |
| --- | --- | --- | --- | --- | --- |
| **Active assistant audio:** after a non-empty `response.output_audio.delta` for response `R` has been observed, non-empty customer speech starts before `R` is terminal | Cancel `R` exactly once. The first ordered post-collision output for `R` must be absent; later output belongs only to the replacement | Bind speech to input turn `I`, cancel to `R`, and replacement response to `I`. Record delta count after cancel and reject stale text/audio | `R=superseded/cancelled`; `I=committed`; replacement response is distinct and processed; session later ends with an explicit success/failure | Event-gated CLI replay releases input from observed audio, validates ordered identities and post-cancel deltas, and runs drop/duplicate/stale-output/clean-wrong negative controls | **Required live minimum.** Real provider scheduling and playback buffering make “speech over audible assistant output” materially different from replay; paced audio, recording, and diagnostics must show the same ledger |
| **Response-created before first audio:** `response.created` for non-terminal `R` is observed, but no assistant audio/text delta has arrived when non-empty speech starts | Cancel `R` exactly once if `R` is still non-terminal at the speech boundary. If `R` reaches a terminal event first, completion wins and no cancel is valid | `R` must be distinguished from the later response even though it emitted no output. `I` gets one commit/user item and the replacement cannot inherit `R`'s identity | Either `R=superseded/cancelled` or `R=completed` when completion won; the interrupting input and replacement/continuation still reconcile | A transport barrier holds the first output, releases speech after response creation, and asserts both cancel-winning and completion-winning ordered variants plus missing/duplicate cancel controls | **Required live minimum.** Provider creation and first-audio scheduling are separate real-clock states; a replay cannot establish that the deployed provider honors this boundary. Risk is a silent lost utterance or a first delta leaking after cancellation |
| **Normal-completion race:** non-empty speech is released at the response terminal boundary, ordered against `response.done` (or the provider's equivalent terminal event) | If terminal response event precedes the speech boundary, no cancel is valid because completion won. If speech precedes terminality while `R` is live, cancel `R` once. Never cancel a response that the ledger already marked terminal | Preserve event order and `R` identity for both sides of the race; do not assign a completion from `R` to the interrupting input or count a no-cancel completion as a dropped barge-in | Completion-winning: `R=completed`, then `I` is a normal next turn. Speech-winning: `R=superseded/cancelled`, then `I` has a distinct response. Both must end explicitly | Deterministic gated transport runs both orderings and negative controls for misattributed completion, missing/duplicate cancel, dropped replacement, hang, and clean-but-unresolved close | **Required live minimum.** This is the narrowest real-time race and is sensitive to provider event ordering; live evidence must state which side won per response rather than claim that every speech boundary requires cancel |
| **Outstanding tool call/result:** tool call `C` is issued by response/turn `R/T` and remains unresolved while customer speech collides with the applicable response or turn | Apply the active-response rule to `R`. Independently, `C` and its result must reach one explicit disposition; cancellation of `R` does not silently dispose of `C` | Correlate `C` and result by `tool_call_id` and owning turn. Reject wrong-call, orphaned, duplicated, and pending-result associations. Do not close while `C` has no disposition | Result delivered once; explicitly rejected/cancelled once with a reason; or another documented product-valid terminal disposition. `R`, `T`, and the session must also be terminal | Public CLI scenario uses a named tool barrier, releases the result at observed event boundaries, and checks delivered/discarded/rejected evidence plus premature-close, loss, orphan, duplicate, wrong-ID, and non-terminal-wait controls | **Deterministic baseline; live only if bounded and reliable.** A live tool call adds model tool-selection and execution timing that the harness cannot event-gate end-to-end without making the result unreliable or billable. The product risk is high, so the PR must report this limitation explicitly; a bounded live tool run may be added only when its readiness gates are met |
| **Repeated interruptions:** at least two non-empty interruptions occur in one continuing session, with each interruption landing against the response state selected by the preceding row | Per interruption, cancel only the still-live response that its ordered boundary supersedes. No response receives a second cancel; a completion-winning boundary receives none | Every input, response, cancel, output delta, and tool disposition has a distinct owner and ordinal. Later input must remain usable after earlier cancellation and must not be attributed to stale state | Each superseded response is terminal, each interrupting input is committed once, each replacement is distinct and processed, and the session reaches one truthful terminal outcome | A bounded CLI run combines active-audio, pre-first-audio, and completion-shaped turns; identity-aware counts reconcile and mutation controls exercise drop/duplication/stale/corrupt/hang/clean-wrong paths | **Required live minimum.** Same-session continuation exposes state-reset and accumulation bugs that isolated runs hide. Live evidence must list every response/input/cancel in order and show a later successful turn |

## Coverage decisions

Replay is a lower-bound proof: it proves that the shipped CLI and validators
handle a deliberately constructed event order and that named failure shapes are
observable. It does not prove real provider scheduling, audio pacing, or the
probability of a collision. Therefore a passing replay must never be described
as complete live evidence.

The minimum live set is active assistant audio, response-created/before-first-
audio, the start/completion boundary, and repeated same-session continuation.
Those shapes are selected because they can lose customer input or leak stale
output at distinct real-clock boundaries even when the deterministic ledger is
correct. Outstanding-tool behavior is mandatory deterministic coverage. It is
live coverage only after the run can hold a named call, release its result at a
bounded observable boundary, and classify delivery or explicit cancellation
without depending on unbounded model behavior. Otherwise the deterministic-only
limitation is part of the live evidence and PR record.

This lane does not claim provider parity, WebRTC or device behavior, echo
cancellation, latency SLOs, or unlimited interruption endurance. It proves the
listed customer contract through the shipped session CLI and reports
inconclusive live outcomes (authentication/setup failure, unavailable provider,
timeout, zero turns, or missing boundary observation) as inconclusive rather
than as passes.

