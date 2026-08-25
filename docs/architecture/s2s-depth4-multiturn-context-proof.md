# s2s depth-4 milestone — multiturn context-carrying conversation is ACHIEVED

Status: **achieved and formally proven in-repo** (2026-08-24).

The depth-4 milestone of the s2s program — a multi-turn spoken conversation
that holds context across turns — is no longer limited to informal live
verification. It is proven by a hermetic, deterministic, CI-gating test with
no network and no credentials.

## What is proven

The committed fixtures
`agent-cli/test/integration/testdata/multiturn_zephyr_4turn.session.json`
(positive) and `multiturn_zephyr_no_carry.session.json` (negative control)
encode a four-turn audio-in conversation over the OpenAI Realtime wire format:

1. **Turn 1 states a fact:** "Remember the word ZEPHYR." The recorded reply
   acknowledges it ("ZEPHYR noted. I will remember it.").
2. **Turn 2 is unrelated filler:** "What is the weather like?"
3. **Turn 3 asks for the fact back:** "What was the word?" The recorded reply
   depends on turn 1: "The word was ZEPHYR."
4. **Turn 4 follows up on the answer:** "Spell that word backwards." Reply:
   "Backwards it is RYHPEZ."

Each turn streams real PCM16 frames (committed per-turn corpus WAVs,
`multiturn_turn1..4.wav`) through `input_audio_buffer.append`, commits, and
requests a response; each turn emits one `response.output_item.added`
conversation-item event plus its response text deltas, so item accumulation
across turns is assertable.

`TestSessionCommandMultiturnReplayCarriesFactAcrossTurns`
(`agent-cli/test/integration/session_multiturn_context_test.go`) drives every
turn through the shipped CLI session command surface (`session --replay ...
--audio-in ...`, one invocation per turn over the same committed fixture
wiring) and asserts content-level cross-turn context carry:

1. Every turn completes cleanly against the replay transport.
2. The turn-3 output carries the codeword introduced in turn 1, and the
   turn-4 output carries its follow-up answer — not merely that N turns
   completed.
3. Conversation items accumulate monotonically across turns (one item event
   per turn in fixture order; the accumulated transcript grows at every turn
   without losing earlier turns' replies).

The repo's fixture-hygiene policy
(`go-llm-gateway/pkg/testing/session-fixture-authoring.md`) forbids committing
raw audio payloads inside `.session.json` captures, so the committed fixtures
redact only the base64 frame bytes; the test restores the exact frames of the
committed corpus WAVs before replaying, mirroring the depth-3 proof's runtime
wire-capture assembly.

## Negative control proves non-vacuous coverage

`TestSessionCommandMultiturnNegativeControlFailsContextAssertion` runs the
exact same CLI drive and assertion against `multiturn_zephyr_no_carry.session.json`,
which differs from the positive fixture only in that its turn-3/turn-4
recorded responses omit the carried fact (a helper also pins that both
fixtures are structurally identical). The assertion fails there and names the
missing codeword, while the positive proof still passes alongside it in one
suite run.

## Historical context

Live captures had shown `conversation.item` events accumulating per turn, but
nothing CI-gating proved multi-turn audio-in sessions or semantic cross-turn
context carry. The proof of record is the hermetic test named above.
