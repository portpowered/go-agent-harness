# Concurrent Session Capacity — Measured Ceiling

## Result (2026-08-23)

| Metric | Value |
| --- | --- |
| Highest fully-clean concurrent session count | **64** |
| First failing rung | 128 sessions |
| First binding resource | Coordination wall-clock throughput: the run's failure-only watchdog (`concurrentRunBudget`, 120s) expired with ~1026 live goroutines while engine pipelines under `-race` could not drain 128×3 turns in budget. Not a correctness failure — no leakage, ordering, or lifecycle violation was observed at any rung. |
| Machine class | Apple M-series laptop-class core, darwin, Go race detector enabled (`CGO_ENABLED=1 go test -race`) |

Rung progression (all clean unless noted): 8 → 16 → 32 → 64 clean; 128 exceeded the
120s coordination budget.

## Method

The on-demand ramp `TestLongConcurrencyCeilingRamp` (since retired; see Reproduction)
doubled the session count from the
contract floor (8) and drives each rung through the same tick-scheduled driver used by
the required proofs: one shared deterministic clock, per-session replay transports and
capture sinks, three scripted turns (text, audio, tool) per session. A rung is "clean"
only when every session completes all three turns, ends its lifecycle on LOOP.END, and
passes the cross-session isolation checker over its full capture. Process counters
(goroutines, heap) are sampled between rungs and reported only; they never pace or gate
anything.

## Reproduction

The ramp was a measurement, not a gate: its only failing condition was the
8-session contract floor, which `TestConcurrentSessionsCompleteScriptedTurns`
proves on every pull request, so it was removed instead of being kept as a
suite no CI job ran. To re-measure, restore
`go-agent-loop/test/functional/sessions/session_ceiling_ramp_long_test.go` from
history and run `go test -race -run '^TestLongConcurrencyCeilingRamp$'`.

## Interpretation

Within the measured range the agent loop sustains at least 64 fully-isolated concurrent
speech-to-speech sessions with zero cross-session leakage of audio frames, transcripts,
deltas, or tool calls. The first limit hit is aggregate pipeline throughput under the
race detector against the fixed coordination watchdog, not session-state isolation:
per-session engines each tick on their own wall-clock floor, so total CPU work grows
linearly with session count. Raising the ceiling further would mean increasing the run
budget or batching engine ticks; both are out of scope for the isolation contract.
