# Fast session regression workflow

Start with the failing test and its full diagnostic output. Fix its cause, run that
test, then broaden verification only for the behavior the change can affect. A
passing local run is not a substitute for the exact-head hosted CI gate.

## Local feedback

For one fixture or helper change, run the named test from its owning Go module.
Keep its subtests and negative controls. Use normal mode first; add race mode for
concurrency/lifecycle changes and coverage mode for instrumentation-sensitive
recording or timing regressions. Record the source revision, OS, mode and count.

For a change spanning session/provider/device boundaries, run the accumulated
historical cohort from the repository root:

```sh
COUNT=1 scripts/test-session-ci-regressions.sh normal
```

For concurrency and instrumentation verification:

```sh
COUNT=1 scripts/test-session-ci-regressions.sh race
COUNT=1 scripts/test-session-ci-regressions.sh coverage
```

Use all modes for a stabilization release or a demonstrated cross-mode failure:

```sh
COUNT=1 scripts/test-session-ci-regressions.sh all
```

The default count is three when COUNT is omitted. Increase it for an identified
intermittent failure, not mechanically after every edit. Once the relevant checks
pass, commit/push and let the CI script own full-suite polling; do not occupy an
executor by repeatedly rebuilding and running the entire CI matrix locally.

Use `make architecture-size-check` when a change affects both service boundaries
and size budgets. It shares one repository inventory while enforcing both rule
sets. The individual `architecture-check` and `size-check` targets remain available
for focused diagnosis; `verify-architecture` also checks fixtures and Wire output.

## What the cumulative runner covers

The runner preserves 17 historical CLI integration scenarios and all their
subtests, the separate CLI interruption-order test, simulated-device regressions,
and composed OpenAI provider/agent-loop tests. It includes the failures retained
through hosted run34174519177. Keep newly discovered regressions in this cohort;
never replace the last failure name and discard earlier cases.

Each mode reports the package and test names and retains failure status even if a
later package succeeds. CLI tests use the microphone stub and loopback/scripted
providers. Normal/coverage disable CGO; race enables it. Coverage mode instruments
tests but does not enforce repository floors: make coverage owns that measurement,
including external CLI/embedding callers and unioned source blocks.

Existing package/test/child deadlines remain bounds. Wait for actual protocol,
queue, playback or lifecycle completion rather than increasing those bounds. A
mock provider must observe a client request before acknowledging its response;
use explicit response identities and exact expected wire sequences. Negative
assertions need an observed completion boundary, not merely a quiet sleep.

## Handoff and evidence

Preserve the complete failed-job output before pushing another candidate. The
executor retains ordinary repairs through CONTINUE. The CI script pins the
submitted SHA and polls required checks; independent review checks that same SHA
and uses a guarded merge. Meta then builds and probes the integrated runtime and
inspects qualitative acceptance. Unit tests alone do not prove the shipped binary
runs, and a baseline merge does not complete the audio-runtime project.

Read [the stabilization record](architecture/session-ci-stabilization.md) for
causes, fixes, repeated-test evidence and remaining limits. That record supersedes
older findings that left response correlation and interrupted playback unresolved.
Physical-device and live Realtime acceptance still require their separate evidence.

When optimizing latency, compare the same tests, inputs and build mode before and
after; distinguish compilation from warm test execution. Keep PCM/order/error
assertions and meaningful stress trials. Prefer simpler semantic barriers or safe
shared setup over shorter sleeps, hidden skips or new orchestration layers.
