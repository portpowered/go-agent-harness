# Repeated barge-in — message-count reconciliation proof (s2s v3c)

## Claim

Within one session, at least three user barge-ins land against in-flight
responses, and the conversation bookkeeping reconciles exactly:

1. **Cancel exactly once** — every `response.cancel` terminates exactly one
   live in-flight response: no double cancels, no stray cancels, and no
   response left streaming when the session ends.
2. **No loss** — every committed user utterance survives: the cumulative
   committed-turn count equals the explicitly expected count.
3. **No duplication** — every delivered assistant turn appears exactly once,
   cancelled turns emit zero post-cancel deltas (no partial assistant content
   re-emitted after its cancel), and the created-response total matches the
   declared composition.

## How v3c enforces it

The lane is enforced by CLI-verified hermetic replay at the T1 tier: scenarios
drive the real `agent probe run` command over recorded session fixtures; no
internal Go function calls count as evidence. The observation is derived from
the recorded event stream by `deriveBargeInObservation`
(`agent-cli/internal/cli/probe.go`), which walks the fixture in order and
counts:

- **User turns committed** — client `input_audio_buffer.commit`, or a client
  `conversation.item.create` carrying a message item.
- **Responses created / cancelled / delivered** — server-to-client
  `response.created`; client `response.cancel` (a cancel with nothing live or
  already-cancelled is *spurious*); `response.done` on an uninterrupted
  response.
- **Post-cancel deltas** — transcript deltas observed after their response was
  cancelled or outside any live response window.
- **In flight at end** — any response still streaming when the fixture ends.

Two measurable expectation kinds were added to the probe runner vocabulary:

- `barge_in_cancel_once` / `barge-in-cancel-once` — asserts spurious cancels
  are zero, nothing is left in flight, distinct cancelled responses equal
  cancel events, and (when `count` is declared) the interruption total equals
  that exact number.
- `message_counts_reconcile` / `message-counts-reconcile` — compares the
  declared composition string against observed counters; keys are
  `user_turns`, `assistant_delivered`, `responses_created`,
  `cancelled_responses`, `cancel_events`, `post_cancel_deltas`. The failure
  names each diverging key with expected-vs-actual values.

## Positive composition

`s2s-v3c-barge-in-repeated` declares exactly:

```
user_turns=7,assistant_delivered=4,responses_created=7,
cancelled_responses=3,cancel_events=3,post_cancel_deltas=0
```

Fixture shape per interrupted turn: append+commit opens a user turn whose
response streams one partial delta before `input_audio_buffer.speech_started`
+ `response.cancel` interrupt it; the interrupting utterance appends+commits
and its replacement response runs `created -> delta -> done`. A closing turn
completes cleanly. That yields 7 committed user turns (3 initial + 3
interrupting + 1 closing), 7 responses (3 interrupted + 3 replacements + 1
closing), 4 delivered assistant turns, and zero post-cancel deltas.

## Fixtures and outcomes

All fixtures live in `agent-cli/test/integration/testdata/`; scenario IDs
match fixture stems:

| fixture | scenario | expected outcome |
|---|---|---|
| `s2s-v3c-barge-in-repeated.session.json` | `s2s-v3c-barge-in-repeated` | passes: 3 interrupts cancelled exactly once, counts reconcile, clean exit |
| `s2s-v3c-barge-in-repeated-dropped-commit.session.json` | `...-dropped-commit` | negative control: loses one committed turn → fails naming `user_turns: expected 7, actual 6` |
| `s2s-v3c-barge-in-repeated-double-cancel.session.json` | `...-double-cancel` | negative control: cancels one response twice → fails `barge-in-cancel-once` (`stray or duplicate cancels`) |
| `s2s-v3c-barge-in-repeated-duplicated-turn.session.json` | `...-duplicated-turn` | negative control: re-emits one delivered turn → fails naming `assistant_delivered: expected 4, actual 5` |

Runtime-mutated controls in
`agent-cli/test/integration/s2s_v3c_barge_in_repeated_test.go` apply the same
violations to copies of the pristine fixture at test time (duplicate a
delivered message block, drop a commit, duplicate a cancel) and must fail the
matching assertion through the same CLI path.

## Reproduction

```sh
# Probe-package unit coverage (registration + evaluation semantics)
go test ./go-agent-loop/pkg/probe -run 'TestS2SV3C' -count=1 -v

# CLI replay derivation + entrypoint behavior
go test ./agent-cli/internal/cli -run 'TestProbeRunS2SV3C|TestDeriveBargeInObservationCountsV3CComposition' -count=1 -v

# Integration suite over the committed fixtures (real CLI exec)
go test ./agent-cli/test/integration -run 'TestS2SV3C' -count=1 -v

# Direct CLI surface, single positive case
(cd agent-cli && go run ./cmd/agent probe run s2s-v3c-barge-in-repeated \
  --replay test/integration/testdata --json)
```

## Observed results (2026-08-26)

Positive case replay observation, derived by the CLI from the committed
fixture:

| counter | observed | declared |
|---|---|---|
| user_turns | 7 | 7 |
| assistant_delivered | 4 | 4 |
| responses_created | 7 | 7 |
| cancelled_responses | 3 | 3 |
| cancel_events | 3 | 3 |
| post_cancel_deltas | 0 | 0 |

Spurious cancels 0, nothing left in flight at session end, terminal reason
`synthetic`. All four registered cases behave as the table above predicts
through `probe run --replay ... --json`: the positive case exits zero; each
negative control exits non-zero naming its violated invariant.
