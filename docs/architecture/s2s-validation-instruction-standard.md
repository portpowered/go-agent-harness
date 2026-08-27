# Standard: how to write validation/hardening instructions

## Why this exists

On 2026-08-27 the operator asked for a validation-replay ticket proving that
an async tool result arriving during unrelated speech is handled correctly:
`s2s-validate-async-tool-result-interrupts-speech`. It merged, with a real,
well-verified integration test (`s2s_async_tool_result_interrupts_speech_test.go`)
and a clean design doc (`s2s-async-tool-result-interrupts-speech.md`).

The same day, a live adversarial probe against the real OpenAI Realtime API
found that an outstanding tool call can still be **silently lost** — the
session closes cleanly (`client_close`, exit 0) while a `sleep("6s")` tool
call is still in flight, and zero `function_call_output` is ever delivered to
the provider. No error. No warning. A customer would have no way to know a
tool call vanished.

Both are true at once: the merged test is genuinely correct, well-verified
work, AND the underlying product defect it was meant to catch is still
present. This document exists so that gap does not repeat. It is not about
that one ticket — it is about how *any* validation/hardening instruction gets
written from here on.

## Root cause, in the instruction's own words

The original ticket told the worker:

> assert exactly what 'correct' means for this runtime (check
> `session_live.go`/`session_runtime_*.go` for the actual designed
> behavior... and make the test assert that real contract rather than
> inventing one)

and defined the negative control as: fail if the result is "silently
dropped, corrupts the in-flight audio, **or wedges the session (no terminal
event ever arrives)**."

Two separate mistakes, compounding:

1. **"Derive correctness from what the code currently does" is a trap when
   the code has not been independently verified correct.** The worker did
   exactly what it was told: read the runtime, found its actual disposition
   ("queue/sequence" — the current audio response finishes, then the result
   is delivered, then a continuation fires), and proved that disposition
   holds under stress. That is real, valuable engineering. But if the
   runtime's actual behavior in some OTHER situation is a bug, telling the
   worker to "assert the real contract you find in the code" just formalizes
   that bug as a passing test. The instruction never asked the worker to
   independently judge whether the behavior it found was *correct*, only to
   verify it was *consistent*.
2. **The negative control's failure taxonomy had a gap.** "Dropped, corrupts
   audio, or wedges with no terminal event" sounds exhaustive but is not. A
   clean `client_close` with exit 0 **is** a terminal event. It does not
   trip any of the three named failure shapes. The actual defect — the
   session closes successfully and confidently while a tool call is still
   genuinely outstanding — is a fourth shape: **clean, confident, wrong
   termination.** An instruction that does not name this shape will not
   produce a test that catches it.

A third, structural factor made both of the above worse: the proof was
built on a hermetic **replay** fixture. A replay fixture only ever exercises
the exact collision its author hand-constructs. The merged fixture correctly
models "tool result eventually arrives while unrelated audio streams" — but
never modeled "the session's own turn-completion bookkeeping decides the
turn is done, and closes, before the tool call resolves at all," because
nobody scripted that specific race. The live probe found it not by being
smarter, but by running against real timing, where that race just happens
(turn N+1 committed 4ms after turn N's `response.done` in the observed
capture) without anyone having to think of it first.

## Target shape for validation/hardening instructions

When the point of an instruction is to find or rule out a bug — not to
document already-trusted behavior — write it so the worker cannot pass by
formalizing a defect. Concretely:

1. **State the correctness contract yourself, independently of the code.**
   Write what SHOULD happen in plain product terms before the worker ever
   opens the implementation — e.g. "an outstanding tool call must either be
   resolved and delivered to the provider, or its loss must be observably
   reported; the session must never report clean success while silently
   discarding it." If you genuinely do not know what should happen and need
   the worker to determine it, say so explicitly and ask the worker to
   justify the contract against the *product's* intent (what a customer
   would reasonably expect), not against "what the code currently does."
   "Read the code to find the actual behavior" is fine for **documenting**
   an already-trusted contract; it is the wrong instruction for **testing**
   one that has not been independently verified.
2. **Enumerate failure shapes exhaustively, and explicitly include "succeeds
   for the wrong reason."** Do not stop at drop / corrupt / hang. Add: does
   it terminate successfully while silently skipping something it should
   have done? Does it report a misleading terminal reason? A negative
   control list that only names loud failures (crash, hang, wrong bytes)
   will not catch a quiet one (clean exit, wrong outcome). If you cannot
   enumerate every shape, say explicitly in the instruction that the worker
   must widen the list rather than treat your list as complete — mirroring
   the Monitor tool's own "coverage — silence is not success" principle
   used elsewhere in this project's tooling.
3. **Name the specific race/collision you actually want proven, not just
   the general area.** "Async tool result during unrelated speech" turned
   out to admit at least two structurally different collisions: (a) result
   arrives while unrelated audio streams, both eventually resolve correctly
   — what got proven; (b) the session's own completion logic closes before
   the result resolves at all — what was actually broken. When an area has
   more than one plausible collision, either name all of them explicitly as
   separate required scenarios, or explicitly instruct the worker to
   enumerate the distinct collisions first and justify why the one(s) it
   picked are the highest-value or most representative, rather than letting
   a hermetic fixture author narrow the scope by default to whichever single
   collision is easiest to script.
4. **Treat a hermetic/replay proof as a lower bound, not a substitute for
   live confirmation, for anything involving real timing/concurrency.** A
   replay fixture proves "this exact scripted sequence is handled
   correctly." It cannot prove "no problematic sequence exists" — that
   requires either real live timing (a probe, per this project's live-probe
   methodology from 2026-08-26/27) or a fixture author who already knows
   every collision worth scripting. For anything where the suspected defect
   is timing/ordering-dependent (races, premature termination, ordering
   between async event sources), the instruction should say plainly that
   the replay proof is necessary but not sufficient, and that a live
   confirmation pass is the actual closing evidence — do not let a green
   hermetic test alone stand in for "this is fixed" when the defect class
   is inherently about real-world timing.
5. **Write the acceptance bar as "prove X is false," not "prove the system
   passes."** Phrase the deliverable as attempting to break the specific
   thing, with the passing condition being "could not break it under this
   precise stress, and here is exactly what was tried" — not "add a test
   that passes." This one framing change is most of what separates the
   10-probe live adversarial swarm (6 of 10 found real, previously-unknown
   bugs) from a validation ticket whose worker's implicit goal was a green
   check mark.

## Quick self-check before submitting a validation/hardening ticket

- Did I write the correct behavior myself, or did I tell the worker to go
  find it in the code under test? If the latter, is that genuinely safe
  here (documenting trusted behavior) or a trap (testing unverified
  behavior)?
- Does my failure list include "succeeds for the wrong reason," not just
  crash/hang/corrupt?
- If this area could plausibly break in more than one distinct way, did I
  name them, or did I let one collision stand in for the whole area?
- If the suspected defect is timing/concurrency-shaped, did I say plainly
  that a replay-only proof is not sufficient closing evidence on its own?
- Would a worker reading this instruction have an easier time writing a
  test that formalizes existing behavior than one that genuinely tries to
  break it? If so, rewrite it.

## See also

- `factory/workstations/ideafy/AGENTS.md` — "Before you submit" and
  "Periodic taxonomy audit" sections cover adjacent but distinct failure
  modes (re-admitting already-merged work; whole categories never entering
  the queue). This document covers a third: work that gets submitted,
  planned, built, and merged, while still not proving what it was meant to
  prove.
- `docs/architecture/s2s-async-tool-result-interrupts-speech.md` — the
  worked example this standard is derived from. Read it alongside this
  document to see exactly what a real, well-verified, but scope-narrower-
  than-intended proof looks like.
