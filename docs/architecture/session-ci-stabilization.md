# Session CI stabilization before factory handoff

The factory remains paused until PR #398 passes the complete CI suite and is
merged. A worker retry is not evidence that a failing candidate is healthy.

## Failure mechanisms and regression ownership

- Provider media and normalized messages are independently consumed. Recording
  previously assigned late PCM to a mutable current turn, creating phantom turns
  and complete-but-empty recordings. The evidence service now joins summaries by
  explicit response ID while retaining raw admission order and PCM offsets.
  `TestRecordedResponseAudioIsIndependentOfQueueScheduling` enumerates all 15
  order-preserving interleavings of two PCM frames and four response events. It
  failed 11 interleavings before the fix; all pass after the fix.
- Interrupts can discard a queued response-end boundary. Inbound media now carries
  an interrupt epoch, and file playback resets conversion when the epoch changes.
  `TestInterruptedPlaybackResetsEvenWhenQueuedEndWasDiscarded` reproduces the
  previous stream-identity error without scheduling sleeps.
- Provider response identity remains useful without an item ID. Media preserves
  that identity, and correction evidence joins actual media boundaries by ID.
  `TestCorrectionBindsMediaBeforeNormalizedResponse` checks both matching and
  unrelated IDs when normalized messages arrive after cancellation. The full
  Family B correction scenario passed 100 coverage-instrumented repetitions.
- Provider input errors remain terminal failures while already-ordered messages
  drain. Device failures still cancel promptly. The corrupt-audio CLI regression
  checks the actual transcript and terminal classification, replacing private
  predicate tests that merely mirrored cancellation implementation.

Earlier stabilization in this PR replaces sleep-driven provider fixtures with
observed protocol/PCM boundaries, separates hold-tone evidence from exact provider
PCM, and admits tool continuations before synchronous provider responses arrive.
The cumulative reproduction command is:

```sh
COUNT=3 bash scripts/test-session-ci-regressions.sh all
```

It runs normal, coverage and race modes with the existing bounded test deadlines.
It includes every subtest of the historical failing scenarios, including stress
trials. It uses fake providers and devices, not customer credentials or hardware.

## Coverage without duplicated fixture catalogs

`make coverage` instruments each module and includes runtime coverage from the
CLI and the independent `tests/embedding` consumer. The coverage gate unions
source blocks across test binaries; repeated blocks do not multiply the statement
count. Complementary executions count once, and inconsistent statement counts
are rejected as incompatible profiles.

The committed manifest is checked against discovered workspace packages. A second
492-line copy of that manifest and tests requiring deleted package names or a
zero-percent floor were removed. Small synthetic profiles continue to test floor,
missing-registration and malformed-input failures.

52 zero floors in the affected services are replaced by measured, conservative
nonzero floors. Pre-stabilization positive floors are not reduced. The new mouse-service floor
is 85%, below both measured Linux (89.30%) and macOS (98.72%) coverage. Public room admission
coverage exercises strict document decoding, conflicting paths, provider
normalization and credential-reference isolation through the service Wire entry
point. Room manifest decoding still has only about 36% coverage; this is an
explicit follow-up area, not a claim of exhaustive validation.

`make coverage-changed` now aliases the full behavioral coverage gate: a service
floor includes external callers, so package-only runs cannot measure it correctly.
This trades local speed for one consistent measurement contract. No additional
scheduler or policy engine is introduced.

## Verification and acceptance limits

Full local coverage passed for 172 registered packages across seven profiles.
Affected playback, recording, live-session, media-gate and observation packages
passed three race repetitions. Recording integration regressions passed 20
repetitions; Family B and corrupt-audio scenarios passed their targeted repeats.
Final hosted CI must pass on the exact merge head before resuming workers.

A local compilation attempt exhausted disk space; disposable old Go build cache
was cleared and affected checks were rerun. That attempt is not counted as a test
pass. Hermetic tests do not claim physical microphone/speaker or live Realtime
model validation. The meta-planner must still inspect the broader migration and
run acceptance probes after each completed vertical.

## Hosted follow-up at 3ff17b64

Run 34170296779 passed hermetic, race, unit, Windows, macOS and WebMCP jobs.
It exposed missing response IDs in two scripted providers, asynchronous final
error publication, a shutdown/write race, three moved-string lint errors and the
platform-dependent mouse coverage floor. The complete failed-job log was retained
before another push; no failing job was waived.

The recording fixtures now emit their known response IDs on audio and transcript
events. Mixed identified/legacy PCM uses the actual artifact offset, with a
regression asserting exact bytes and per-turn offsets. The runtime samples the
provider terminal error directly after joining the loop, rather than depending on
a notification goroutine. A disabled-tool replay negative failed twice in 500
repetitions before that change and passed 500 after it. Public embedding tests
check both the returned error and terminal evidence. Media failure reporting
precedes visibility to playback, and finalization samples the cause after drain.

The session-log offset/count remains a convenience summary for contiguous response
PCM. Per-frame audio.frame records retain exact admission order, byte offsets and
provider identity and are the authoritative evidence for unusual interleaving.
