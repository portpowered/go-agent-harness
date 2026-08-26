# PRD: s2s-v4f-tool-during-audio — a tool call issued while audio is still streaming in does not corrupt or drop the in-flight audio turn

## Introduction

This lane implements the vertical from `docs/architecture/s2s-lane-manifest.md`:
`s2s-v4f-tool-during-audio` (history: NEVER SUBMITTED) — "a tool call issued
while audio is still streaming in does not corrupt or drop the in-flight audio
turn."

Per existing s2s lane conventions in this repo (see
`agent-cli/test/integration/session_tool_single_call_test.go` for v4a and the
v2x audio-in lanes), this is proven as a narrow, hermetic (T1) vertical slice
driving the real `agent session` CLI path over the record/replay transport,
with a scripted provider exchange that interleaves an in-flight output-audio
turn with a named function tool call. The observable outcome under test:
the audio deltas that were already streaming when the tool call arrives are
delivered completely and uncorrupted (correct count, order, and content), the
tool call traverses the session path, and the turn still terminates cleanly.

**Scope note:** one narrow vertical slice. Do not expand scope to neighboring
lanes (`s2s-v3b-barge-in-tool-result-contract`, `s2s-v4b-tool-parallel-calls`,
`s2s-v4c-tool-error`, `s2s-v4d-tool-timeout`, `s2s-v7a-metrics-modality`).
Before merging, verify against origin/main that this lane's outcome is genuinely
present and covered by tests.

## Context

**Customer ask:** implement the lane per the manifest so that a tool call issued
while audio is still streaming in neither corrupts nor drops the in-flight audio
turn, verified by tests following existing s2s conventions.

**Concrete problem:** in a speech-to-speech session, the model can emit a
function tool call while output audio deltas are mid-stream. If any layer of the
session runtime drops, reorders, or truncates buffered audio on encountering the
interleaved tool event, the user hears clipped or garbled speech around every
tool use. Nothing on main currently proves the interleaving is handled: no test
exercises a tool call arriving between streamed audio deltas of the same
response.

**High-level solution:** add a deterministic record/replay fixture whose
server-to-client side streams part of an output-audio turn, then delivers one
named function tool call, then continues the remaining audio deltas of the same
response before terminal completion. Drive it through the public CLI surface and
assert, from recorded artifacts: exact audio delta count/order/content across
the interleaving boundary, presence of the tool call in the replayed exchange in
order, non-empty post-turn state, clean termination, plus a negative control
that proves the assertions detect a corrupted/dropped turn deterministically.

## Project-level acceptance criteria

- A hermetic integration test proves that when a named tool call is interleaved
  mid-way through a streamed output-audio response, all audio deltas of that
  response are observed intact (count, order, PCM content) through the public
  CLI path.
- The interleaved tool call is observably present in the replayed provider
  exchange, in order relative to the surrounding audio deltas.
- The turn terminates cleanly with a completed/terminal event and no wedged
  buffer or hang.
- A negative control proves the assertions fail deterministically when the
  in-flight audio is corrupted or dropped at the interleaving point.
- No production behavior regression: all pre-existing tests on main still pass.
- Quality gate: typecheck passes; tests pass; no NEW lint violations relative to
  current main, and the gates green on main (backend-size, pkg-maint,
  pkg-file-count, pkg-structure, vet) stay green.
- Delivery loop completed: PR merged per the Delivery Loop section below.

## Goals

- Prove the manifest claim behaviorally through the public CLI surface, at the
  same test tier (hermetic T1, record/replay) as sibling lanes.
- Reuse existing committed corpus WAV fixtures and fixture-builder conventions;
  add no new binary assets.
- Deterministic evidence with an explicit negative control.

## User Stories

### s2s-v4f-tool-during-audio-001: Interleaved tool/audio record-replay scenario fixture
**Description:** As a developer, I need a deterministic record/replay fixture
that streams output audio deltas, interleaves exactly one named function tool
call mid-stream within the same response, then resumes the remaining audio
deltas and completes, so the interleaving is reproducible in tests.

**Acceptance Criteria:**
- [ ] Fixture builder follows the conventions of `session_tool_single_call_test.go`: client side expects paced frames of a committed corpus WAV via input_audio_buffer.append/commit/response.create; server side delivers partial audio deltas -> one named function tool call (name + arguments) -> remaining audio deltas of the same response -> terminal completed response
- [ ] Fixture is fully deterministic (no wall-clock/network dependence) and reuses existing committed corpus audio; no new binary assets added
- [ ] Typecheck passes

### s2s-v4f-tool-during-audio-002: In-flight audio turn survives an interleaved tool call
**Description:** As a user speaking to the agent, I want my assistant's spoken
reply to remain complete and intelligible even when the model issues a tool call
mid-sentence, so I never hear clipped or corrupted speech.

**Acceptance Criteria:**
- [ ] Driving the real `agent session` CLI over the fixture yields every expected output-audio delta of the response, in order, with byte-exact PCM content across the tool-call interleaving boundary (none missing, duplicated, reordered, or truncated)
- [ ] The interleaved tool call (name + arguments) is observably present in the replayed exchange in order relative to the surrounding audio deltas
- [ ] The response terminates cleanly with a terminal/completed event; the session produces a non-empty final state with no hang or wedge
- [ ] Tests pass
- [ ] Typecheck passes

### s2s-v4f-tool-during-audio-003: Negative control detects corruption or drop
**Description:** As a maintainer, I need proof the assertions actually bite, so
a future regression that drops or corrupts the in-flight audio cannot pass the
lane silently.

**Acceptance Criteria:**
- [ ] A variant fixture that corrupts (or drops) audio deltas at the interleaving point causes the test to fail deterministically with a specific assertion message identifying the affected delta range
- [ ] The negative control failure mode is asserted (i.e., the test asserts that tampering fails), matching the negative-control convention used by sibling lanes
- [ ] Tests pass
- [ ] Typecheck passes

## Functional Requirements

- FR-1: A deterministic record/replay fixture interleaves exactly one named
  function tool call between two halves of one streamed output-audio response.
- FR-2: The behavioral test drives the public `agent session` CLI path (same
  entrypoint/tier as sibling s2s lanes) and observes audio deltas only from
  recorded artifacts/emitted events.
- FR-3: Assertions cover delta count, order, and PCM content across the
  interleaving boundary, ordered presence of the tool call, and clean terminal
  termination.
- FR-4: A negative control proves corruption/drop detection.

## Non-Goals

- No barge-in/interruption semantics (v3x lanes).
- No parallel tool calls (v4b), tool errors (v4c), timeouts (v4d), or unknown
  tools (v4e).
- No changes to metrics/token accounting (v7x).
- No production runtime changes unless the test exposes a genuine defect; if it
  does, fix the defect minimally within this lane and note it in the PR.
- No new audio corpus assets.

## Technical Considerations

- Follow `agent-cli/test/integration/session_tool_single_call_test.go` fixture
  conventions (synthetic record/replay capture, scripted server-to-client
  events, paced append frames).
- Keep ownership clear: new test lives alongside sibling s2s integration tests
  in `agent-cli/test/integration/`; no exported contract changes expected.
- Verify against origin/main before merging that the lane's outcome is genuinely
  present and covered by these tests.

## Success Metrics

- One merged PR containing the interleaved-scenario proof plus negative control.
- The lane's manifest claim ("tool call during streaming audio does not corrupt
  or drop the in-flight audio turn") is demonstrated by a passing test on main
  after merge.

## Open Questions

- None. If the interleaving test exposes a genuine runtime defect on main, the
  minimal fix lands in this lane and is called out explicitly in the PR
  description.

## Delivery Loop

The worker/reviewer cycle continues until required CI is terminal and passing,
all blocking PR conversation feedback is explicitly addressed, merge conflicts
are resolved, and the PR is merged. A PR that is merely opened, green, approved,
or ready to merge is NOT complete.

Stage ownership:

- **Implementation stage** finishes when it has pushed its final head, the PR is
  open, CI has started, and all blocking review feedback is addressed. After
  that finish line, implementation must NOT poll or re-check CI — every
  redispatch consumes one of the 12 process visits.
- **Review stage** owns driving CI to terminal-and-passing, resolving merge
  conflicts, and merging the PR. Terminal-and-passing CI, conflict resolution,
  and merge are review-stage outcomes, not implementation-stage
  responsibilities, even though merge remains the lane-wide completion boundary.

Evidence about a CI run goes in a PR comment and never in a commit.
