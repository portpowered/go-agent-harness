# S2S final session metrics observation

The session runtime exposes one supported terminal observation containing the
production accounting result for the completed run. This is the composition
boundary used by the hermetic reconciliation proof in
`agent-cli/test/integration/session_metrics_reconcile_test.go`.

## Contract

Callers install a `services.SessionRuntimeObserver` through the composed CLI
runtime observer port. `SessionRuntimeObservation.FinalAccounting` is nil for
audio and turn-completed observations and is populated exactly once on the
terminal observation. The terminal value is delivered after the session
consumer has drained all events that crossed the production accounting seam,
including clean and error termination paths.

`SessionFinalAccounting` reports session-cumulative `PromptTokens`,
`CompletionTokens`, `TotalTokens`, and `ReasoningTokens`. Every non-negative
provider `MESSAGE.END` usage value is an incremental contribution for its
completed turn and is added once. A cumulative provider reading must be
normalized before it enters the session stream; the runtime does not add the
same cumulative reading repeatedly or guess at de-duplication.

`Metrics` is the runtime-owned `metrics.Snapshot`, not a fold of
`SessionStreamObserver`, a replay ledger, or a rendered artifact. It contains
all eight input/output × audio/text/image/tool series in deterministic order,
the histogram bounds, non-cumulative bucket counts, overflow count, sample
count, and byte sum. Untouched series remain present with explicit zero
counters and histogram state. The snapshot is deep-copied before delivery so
an observer can retain or mutate it without changing runtime state.

## Hermetic proof

`TestSessionCommandMetricsReconcileMatchesIndependentFoldOverFullSession`
executes the normal `agent session` Cobra route over a temporary replay
capture. The capture reuses `go-agent-loop/testdata/audio/utt_short_16k.wav`
for two non-empty output-audio deltas and contains two usage-bearing turns,
one text and one audio, followed by `SESSION.CLOSE` with reason
`fixture_complete`. The expected side independently folds the raw replay
records, including every metric histogram and zero series; the actual side is
only the captured terminal `FinalAccounting` value.

`TestSessionCommandMetricsReconcileMissingOutputTextDeltaFails` first proves
the full independent fold reconciles with the captured production value. It
then removes exactly one non-empty output-text delta from a copy of the
independent ledger, leaves the captured production value unchanged, and runs
the same exact comparator. The verdict reports only the intended
`output/text` series divergence: its event-count and byte-total differences are
(`expected 0`, `actual 1`) and (`expected 0`, `actual 16`), with the matching
histogram bucket difference in that same series. Token totals remain unchanged
because the mutation removes no `MESSAGE.END` usage.

Run the focused proof offline with:

```text
(cd agent-cli && go test ./test/integration -run 'TestSessionCommandMetricsReconcile' -count=1)
```

The replay route requires no credentials or live network access. The proof
does not claim live-provider behavior or approximate reconciliation.
